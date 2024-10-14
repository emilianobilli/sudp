package sudp

import (
	"fmt"
	"net"
	"time"
)

type ClientConn struct {
	server *peer
	Conn
}

func (c *ClientConn) filterPacket(pkt *pktbuff) (*hdr, error) {
	hdr, e := hdrLoad(pkt.head(hdrsz))
	if e != nil || hdr.dst != c.vaddr {
		return nil, newError("invalid header - message drop", e)
	}
	if c.server == nil && c.server.vaddr != hdr.src || c.vaddr != hdr.dst {
		return nil, newError("invalid source - message drop", nil)
	}

	if c.server.tsync == nil {
		if c.server.tsync, e = newTimeSync(hdr.time); e != nil {
			return nil, newError("not in time - message drop", e)
		}
	} else if !c.server.tsync.inTime(hdr.time) {
		return nil, newError("not in time - message drop", e)
	}
	return hdr, nil
}

func (c *ClientConn) serve() error {
	start := make(chan time.Time)
	go func(refresh <-chan time.Time) {

		var (
			start bool
			tries int
		)

		start = true
		tries = 0
		control := time.NewTicker(500 * time.Millisecond)
		for {
			select {
			case msg := <-c.ch.userTx:
				if c.server == nil || c.server.vaddr != msg.addr || !c.server.ready {
					c.ch.errTx <- newError("not ready", nil)
					continue
				}
				e := c.server.sendDataPacket(c.vaddr, msg.buff, c.conn)
				if e != nil {
					c.ch.errTx <- newError("sending data packet:", e)
					continue
				}
				c.ch.errTx <- nil
			case pkt := <-c.ch.netRx:
				if pkt == nil {
					c.err <- fmt.Errorf("unexpected close")
					return
				}
				hdr, e := c.filterPacket(pkt)
				if e != nil {
					log(Error, fmt.Sprintf("filter: %v", e))
					continue
				}
				e = c.server.handlePacket(hdr, pkt, c.private, c.ch.userRx, c.conn)
				if e != nil {
					log(Error, fmt.Sprintf("at package handle - %v", e))
				}
			case <-control.C:
				if c.server.ready {
					epoch, _ := c.server.epochs.current()
					header := newHdr(typeCtrlMessage, uint32(epoch), c.vaddr, c.server.vaddr)
					header.len = ctrlmessagesz
					packet := allocPktbuff()
					packet.addr = c.server.naddr
					if err := header.dump(packet.tail(hdrsz)); err != nil {
						continue
					}
					ctrl := ctrlmessage{
						crc32: header.crc32,
					}
					ctrl.set(KeepAlive)
					if err := ctrl.dump(packet.tail(ctrlmessagesz), c.private); err != nil {
						continue
					}
					packet.pktSend(c.conn)
				}
				if c.server.resend != nil && c.server.hndshk && time.Now().Sub(c.server.hsSent) > 2*time.Second {
					tries = tries + 1
					if tries == 4 {
						c.err <- fmt.Errorf("timeout")
						return
					}
					c.server.hsSent = time.Now()
					c.server.resend.pktSend(c.conn)
				}
			case <-refresh:
				fmt.Println("Refresh ======================================= ")
				tries = 0
				if pending, _ := c.server.epochs.pending(); pending != -1 {
					continue // Evaluar que hacemos aca
				}
				epoch := c.server.epochs.cEpoch + 1
				key, err := c.server.epochs.new(epoch)
				if err != nil {
					continue
				}
				header := newHdr(typeClientHandshake, uint32(epoch), c.vaddr, c.server.vaddr)
				header.len = handshakesz
				packet := allocPktbuff()
				packet.addr = c.server.naddr
				if err = header.dump(packet.tail(hdrsz)); err != nil {
					continue
				}
				handshake := handshake{
					crc32: header.crc32,
				}
				copy(handshake.pubkey[:], key.public())
				if err = handshake.dump(packet.tail(handshakesz), c.private); err != nil {
					continue
				}
				c.server.hndshk = true
				c.server.hsSent = time.Now()
				c.server.resend = packet
				packet.pktSend(c.conn)
			}
			if start && c.server.ready {
				start = false
				tries = 0
				refresh = time.NewTicker(30 * time.Second).C
				c.err <- nil
			}
		}
	}(start)
	fmt.Println("Mandando mensaje")
	start <- time.Time{}
	return <-c.err
}

func Connect(laddr *LocalAddr, raddr *RemoteAddr) (*ClientConn, error) {

	if raddr.NetworkAddress == nil {
		return nil, fmt.Errorf("invalid peer address")
	}
	if laddr.PrivateKey == nil || raddr.PublicKey == nil {
		return nil, fmt.Errorf("keys not present")
	}

	conn, err := net.ListenUDP("udp4", laddr.NetworkAddress)
	if err != nil {
		return nil, err
	}

	c := &ClientConn{
		Conn: Conn{
			vaddr:   laddr.VirtualAddress,
			conn:    conn,
			private: laddr.PrivateKey,
			err:     make(chan error),
		},
		server: &peer{
			vaddr:  raddr.VirtualAddress,
			naddr:  raddr.NetworkAddress,
			pubkey: raddr.PublicKey,
		},
	}
	c.ch.init(c.conn, c.server.naddr)
	c.server.epochs.init()

	if e := c.serve(); e != nil {
		fmt.Println(e)
		c.conn.Close()
		<-c.err
		c.ch.close()
		return nil, e
	}
	return c, nil
}

func (s *ClientConn) Send(buff []byte) error {
	s.ch.userTx <- &message{
		buff: buff,
		addr: s.server.vaddr,
	}
	return <-s.ch.errTx
}

func (s *ClientConn) Recv() []byte {
	msg := <-s.ch.userRx
	return msg.buff
}