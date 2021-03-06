package apns

import (
	"crypto/tls"
	"log"
	"time"
)

var _ APNSClient = &Client{}

// APNSClient is an APNS client.
type APNSClient interface {
	ConnectAndWrite(resp *PushNotificationResponse, payload []byte) (err error)
	Send(pn *PushNotification) (resp *PushNotificationResponse)
}

// Client contains the fields necessary to communicate
// with Apple, such as the gateway to use and your
// certificate contents.
//
// You'll need to provide your own CertificateFile
// and KeyFile to send notifications. Ideally, you'll
// just set the CertificateFile and KeyFile fields to
// a location on drive where the certs can be loaded,
// but if you prefer you can use the CertificateBase64
// and KeyBase64 fields to store the actual contents.
type Client struct {
	Gateway           string
	CertificateFile   string
	CertificateBase64 string
	KeyFile           string
	KeyBase64         string
	NumConnections    int
	certificate       tls.Certificate
	apnsConnection    *tls.Conn
	pool              *ConnectionPool
}

// BareClient can be used to set the contents of your
// certificate and key blocks manually.
func BareClient(gateway, certificateBase64, keyBase64 string) (c *Client) {
	c = new(Client)
	c.Gateway = gateway
	c.CertificateBase64 = certificateBase64
	c.KeyBase64 = keyBase64
	c.NumConnections = 5
	return
}

// NewClient assumes you'll be passing in paths that
// point to your certificate and key.
func NewClient(gateway, certificateFile, keyFile string) (c *Client) {
	c = new(Client)
	c.Gateway = gateway
	c.CertificateFile = certificateFile
	c.KeyFile = keyFile
	c.NumConnections = 5
	return
}

// Send connects to the APN service and sends your push notification.
// Remember that if the submission is successful, Apple won't reply.
func (client *Client) Send(pn *PushNotification) (resp *PushNotificationResponse) {
	resp = new(PushNotificationResponse)

	payload, err := pn.ToBytes()
	if err != nil {
		resp.Success = false
		resp.Error = err
		return
	}

	err = client.ConnectAndWrite(resp, payload)
	if err != nil {
		resp.Success = false
		resp.Error = err
		return
	}

	resp.Success = true
	resp.Error = nil

	return
}

// ConnectAndWrite establishes the connection to Apple and handles the
// transmission of your push notification, as well as waiting for a reply.
//
// In lieu of a timeout (which would be available in Go 1.1)
// we use a timeout channel pattern instead. We start two goroutines,
// one of which just sleeps for TimeoutSeconds seconds, while the other
// waits for a response from the Apple servers.
//
// Whichever channel puts data on first is the "winner". As such, it's
// possible to get a false positive if Apple takes a long time to respond.
// It's probably not a deal-breaker, but something to be aware of.
func (client *Client) ConnectAndWrite(resp *PushNotificationResponse, payload []byte) error {
	// Get the connection pool
	p, err := client.getConnectionPool()
	if err != nil {
		return err
	}

	// Attempt to write to the pool
	c, _, err := p.Write(payload)
	if err != nil {
		return err
	}

	// try to get a response
	go client.getResponse(c)

	// assume success, we'll log any errors
	resp.Success = true

	return nil
}

// Returns a certificate to use to send the notification.
// The certificate is only created once to save on
// the overhead of the crypto libraries.
func (client *Client) getCertificate() error {
	var err error

	if client.certificate.PrivateKey == nil {
		if len(client.CertificateBase64) == 0 && len(client.KeyBase64) == 0 {
			// The user did not specify raw block contents, so check the filesystem.
			client.certificate, err = tls.LoadX509KeyPair(client.CertificateFile, client.KeyFile)
		} else {
			// The user provided the raw block contents, so use that.
			client.certificate, err = tls.X509KeyPair([]byte(client.CertificateBase64), []byte(client.KeyBase64))
		}
	}

	return err
}

// getConnectionPool returns a connection pool used to send message payloads.
func (client *Client) getConnectionPool() (*ConnectionPool, error) {
	if client.pool != nil {
		return client.pool, nil
	}

	err := client.getCertificate()
	if err != nil {
		return nil, err
	} else {
		client.pool = NewConnectionPool(client.NumConnections, client.Gateway, client.certificate)
	}

	return client.pool, err
}

// getResponse attempts to read the response apns from the given connection.
// On error, will print the response.
func (client *Client) getResponse(c *Connection) {
	// Create one channel that will serve to handle
	// timeouts when the notification succeeds.
	timeoutChannel := make(chan bool, 1)
	go func() {
		time.Sleep(time.Second * TimeoutSeconds)
		timeoutChannel <- true
	}()

	// This channel will contain the binary response
	// from Apple in the event of a failure.
	responseChannel := make(chan []byte, 1)
	go func() {
		buffer := make([]byte, 6, 6)
		_, err := c.Read(buffer)

		// on read error, close the connection
		if err != nil {
			c.Close()
		}

		responseChannel <- buffer
	}()

	// First one back wins!
	// The data structure for an APN response is as follows:
	//
	// command    -> 1 byte
	// status     -> 1 byte
	// identifier -> 4 bytes
	//
	// The first byte will always be set to 8.
	select {
	case r := <-responseChannel:
		if r[1] != 0 {
			response := ApplePushResponses[r[1]]
			if response != "NO_ERRORS" {
				log.Printf("APNS error: %s\n", response)
			}
		}
	case <-timeoutChannel:
	}
}
