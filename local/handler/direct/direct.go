package direct

import (
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"strings"
	"time"

	"github.com/getlantern/netx"
	"github.com/yinqiwen/gsnova/common/event"
	"github.com/yinqiwen/gsnova/local/hosts"
	"github.com/yinqiwen/gsnova/local/proxy"
)

type directChannel struct {
	sid            uint32
	conn           net.Conn
	httpsProxyConn bool
	udpProxyConn   bool
	conf           *proxy.ProxyChannelConfig
}

func (tc *directChannel) ReadTimeout() time.Duration {
	readTimeout := tc.conf.ReadTimeout
	if 0 == readTimeout {
		readTimeout = 15
	}
	return time.Duration(readTimeout) * time.Second
}

func (hc *directChannel) HandleCtrlEvent(ev event.Event) {

}

func (hc *directChannel) SetCryptoCtx(ctx *event.CryptoContext) {
}

func (tc *directChannel) Open() error {
	return nil
}

func (tc *directChannel) Request([]byte) ([]byte, error) {
	return nil, nil
}

func (tc *directChannel) Closed() bool {
	return nil == tc.conn
}

func (tc *directChannel) Close() error {
	conn := tc.conn
	if nil != conn {
		conn.Close()
		tc.conn = nil
	}
	return nil
}

func (tc *directChannel) Read(p []byte) (int, error) {
	conn := tc.conn
	if nil == conn {
		return 0, io.EOF
	}
	return conn.Read(p)
}

func (tc *directChannel) Write(p []byte) (n int, err error) {
	conn := tc.conn
	if nil == conn {
		return 0, io.EOF
	}
	return conn.Write(p)
}

func (d *directChannel) read() {
	defer d.Close()
	for {
		c := d.conn
		if nil == c {
			return
		}
		c.SetReadDeadline(time.Now().Add(d.ReadTimeout()))
		b := make([]byte, 1500)
		n, err := c.Read(b)
		if n > 0 {
			var ev event.Event
			if !d.udpProxyConn {
				ev = &event.TCPChunkEvent{Content: b[0:n]}
			} else {
				ev = &event.UDPEvent{Content: b[0:n]}
			}
			//log.Printf("######recv %T", ev)
			ev.SetId(d.sid)
			proxy.HandleEvent(ev)
		}
		if nil != err {
			if !d.udpProxyConn {
				closeEv := &event.ConnCloseEvent{}
				closeEv.SetId(d.sid)
				proxy.HandleEvent(closeEv)
			}
			return
		}
	}
}

func newDirectChannel(ev event.Event, conf *proxy.ProxyChannelConfig) (*directChannel, error) {
	host := ""
	port := ""
	network := "tcp"
	fromHttpsConnect := false
	switch ev.(type) {
	case *event.UDPEvent:
		network = "udp"
		host = ev.(*event.UDPEvent).Addr
	case *event.TCPOpenEvent:
		host = ev.(*event.TCPOpenEvent).Addr
	case *event.HTTPRequestEvent:
		req := ev.(*event.HTTPRequestEvent)
		host = req.Headers.Get("Host")
		fromHttpsConnect = strings.EqualFold(req.Method, "Connect")
	default:
		return nil, fmt.Errorf("Can NOT create direct channel by event:%T", ev)
	}
	//log.Printf("Session:%d enter direct with host %s & event:%T", ev.GetId(), host, ev)
	if len(host) == 0 {
		return nil, fmt.Errorf("Empty remote addr in event")
	}
	if strings.Contains(host, ":") {
		host, port, _ = net.SplitHostPort(host)
	}

	isIP := net.ParseIP(host) != nil

	if !isIP && !hosts.InHosts(host) && hosts.InHosts(hosts.SNIProxy) && port == "443" && network == "tcp" {
		host = hosts.SNIProxy
	}
	useTLS := false
	if conf.ForceTLS && port == "80" && hosts.InHosts(host) {
		useTLS = true
	} else {
		useTLS = false
	}
	if !isIP {
		host = hosts.GetHost(host)
	}

	//log.Printf("Session:%d get host:%s", ev.GetId(), host)
	addr := ""
	if useTLS {
		addr = addr + ":443"
	} else {
		if len(port) > 0 {
			addr = host + ":" + port
		} else {
			if fromHttpsConnect {
				addr = host + ":443"
			} else {
				addr = host + ":80"
			}
		}
	}
	dailTimeout := conf.DialTimeout
	if 0 == dailTimeout {
		dailTimeout = 5
	}
	log.Printf("Session:%d connect %s:%s for %s %T", ev.GetId(), network, addr, host, ev)
	c, err := netx.DialTimeout(network, addr, time.Duration(dailTimeout)*time.Second)
	if nil != err {
		log.Printf("Failed to connect %s for %s with error:%v", addr, host, err)
		return nil, err
	}

	d := &directChannel{ev.GetId(), c, fromHttpsConnect, network == "udp", conf}
	//d := &directChannel{ev.GetId(), c, fromHttpsConnect, network == "udp", toReplaceSNI, false, nil}
	if useTLS {
		tlcfg := &tls.Config{}
		tlcfg.InsecureSkipVerify = true
		sniLen := len(conf.SNI)
		if sniLen > 0 {
			tlcfg.ServerName = conf.SNI[rand.Intn(sniLen)]
		}
		tlsconn := tls.Client(c, tlcfg)
		err = tlsconn.Handshake()
		if nil != err {
			log.Printf("Failed to handshake with %s", addr)
		}
		d.conn = tlsconn
	}
	go d.read()
	return d, nil
}

type DirectProxy struct {
	conf proxy.ProxyChannelConfig
}

func (p *DirectProxy) PrintStat(w io.Writer) {
}

func (p *DirectProxy) Config() *proxy.ProxyChannelConfig {
	return &p.conf
}

func (p *DirectProxy) Init(conf proxy.ProxyChannelConfig) error {
	p.conf = conf
	return nil
}
func (p *DirectProxy) Destory() error {
	return nil
}
func (p *DirectProxy) Features() proxy.Feature {
	var f proxy.Feature
	f.MaxRequestBody = -1
	return f
}

func (p *DirectProxy) Serve(session *proxy.ProxySession, ev event.Event) error {
	if nil == session.Remote || session.Remote.C.Closed() {
		switch ev.(type) {
		case *event.TCPOpenEvent:
		case *event.HTTPRequestEvent:
		case *event.UDPEvent:
		default:
			return fmt.Errorf("Can NOT create direct channel by event:%T", ev)
		}
		c, err := newDirectChannel(ev, &p.conf)
		if nil != err {
			return err
		}
		session.Remote = &proxy.RemoteChannel{
			DirectIO: true,
		}
		session.Remote.C = c
		if c.httpsProxyConn {
			session.Hijacked = true
			return nil
		}
	}
	if nil == session.Remote {
		return fmt.Errorf("No remote connected.")
	}
	switch ev.(type) {
	case *event.UDPEvent:
		session.Remote.WriteRaw(ev.(*event.UDPEvent).Content)
	case *event.ConnCloseEvent:
		session.Remote.Close()
	case *event.TCPOpenEvent:
	case *event.ConnTestEvent:
		//do nothing
	case *event.TCPChunkEvent:
		session.Remote.WriteRaw(ev.(*event.TCPChunkEvent).Content)
	case *event.HTTPRequestEvent:
		req := ev.(*event.HTTPRequestEvent)
		content := req.HTTPEncode()
		_, err := session.Remote.WriteRaw(content)
		if nil != err {
			closeEv := &event.ConnCloseEvent{}
			closeEv.SetId(ev.GetId())
			proxy.HandleEvent(closeEv)
			return err
		}
		return nil
	default:
		log.Printf("Invalid event type:%T to process", ev)
	}
	return nil

}

func init() {
	proxy.RegisterProxyType("DIRECT", &DirectProxy{})
	//proxy.RegisterProxy(&tlsDirectProy)
}
