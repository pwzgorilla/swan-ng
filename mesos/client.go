package mesos

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/golang/protobuf/proto"

	"github.com/bbklab/swan-ng/mesos/protobuf/mesos"
	"github.com/bbklab/swan-ng/mesos/protobuf/sched"
)

// Client represents a client interacting with mesos master via x-protobuf
type Client struct {
	http      *http.Client
	zkPath    *url.URL
	framework *mesos.FrameworkInfo

	eventCh chan *sched.Event // mesos events
	errCh   chan error        // subscriber's error events

	endPoint string // eg: http://master/api/v1/scheduler
	cluster  string // name of mesos cluster
}

// NewClient ...
func NewClient(zkPath *url.URL) (*Client, error) {
	c := &Client{
		http: &http.Client{
			Transport: &http.Transport{
				Dial: (&net.Dialer{
					Timeout:   time.Second * 10,
					KeepAlive: time.Second * 30,
				}).Dial,
			},
		},
		zkPath:    zkPath,
		framework: defaultFramework(),
		eventCh:   make(chan *sched.Event, 1024),
		errCh:     make(chan error, 1),
	}

	if err := c.init(); err != nil {
		return nil, err
	}

	return c, nil
}

// init setup mesos sched api endpoint & cluster name
func (c *Client) init() error {
	state, err := c.MesosState()
	if err != nil {
		return err
	}

	l := state.Leader
	if l == "" {
		return fmt.Errorf("empty mesos leader")
	}
	c.endPoint = "http://" + l + "/api/v1/scheduler"

	c.cluster = state.Cluster
	if c.cluster == "" {
		c.cluster = "cluster" // set default cluster name
	}

	return nil
}

// Cluster return current mesos cluster's name
func (c *Client) Cluster() string {
	return c.cluster
}

// Send send mesos request against the mesos master's scheduler api endpoint.
// NOTE it's the caller's responsibility to deal with the Send() error
func (c *Client) Send(call *sched.Call) error {
	resp, err := c.send(call)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	bs, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if code := resp.StatusCode; code != 202 {
		return fmt.Errorf("expect 202, got %d: [%s]", code, string(bs))
	}

	return nil
}

func (c *Client) send(call *sched.Call) (*http.Response, error) {
	bs, err := proto.Marshal(call)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", c.endPoint, bytes.NewReader(bs))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Accept", "application/json")

	return c.http.Do(req)
}

// ReSubscribe ...
// TODO
func (c *Client) ReSubscribe(call *sched.Call) error {
	// reinit
	// resub
	return nil
}

// Subscribe ...
func (c *Client) Subscribe() error {
	log.Printf("subscribing to mesos leader: %s", c.endPoint)

	call := &sched.Call{
		Type: sched.Call_SUBSCRIBE.Enum(),
		Subscribe: &sched.Call_Subscribe{
			FrameworkInfo: c.framework,
		},
	}
	if c.framework.Id != nil {
		call.FrameworkId = &mesos.FrameworkID{
			Value: proto.String(c.framework.Id.GetValue()),
		}
	}

	resp, err := c.send(call)
	if err != nil {
		return fmt.Errorf("subscribe to mesos leader [%s] error [%v]", c.endPoint, err)
	}

	if code := resp.StatusCode; code != 200 {
		bs, _ := ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		return fmt.Errorf("subscribe with unexpected response [%d] - [%s]", code, string(bs))
	}
	log.Printf("subscribed to mesos leader: %s", c.endPoint)

	go c.watchEvents(resp)
	return nil
}

func (c *Client) watchEvents(resp *http.Response) {
	log.Println("mesos event subscriber starting")

	defer func() {
		log.Warnln("mesos event subscriber quited")
		resp.Body.Close()
	}()

	var (
		dec = json.NewDecoder(resp.Body)
		ev  = new(sched.Event)
		err error
	)

	for {
		err = dec.Decode(&ev)
		if err != nil {
			log.Errorln("mesos events subscriber decode events error:", err)
			c.errCh <- err
			return
		}

		c.eventCh <- ev
	}
}
