package sudp

import (
	"crypto/ecdsa"
	"fmt"
	"net"
	"time"
)

type peer struct {
	epochs epochs
	pubkey *ecdsa.PublicKey
	naddr  *net.UDPAddr // Net Address
	vaddr  uint16       // Protocol virtual address
	ttlm   time.Time    // Time to last message
	tsync  *timeSync
	ready  bool
	hndshk bool
	resend *pktbuff
	hsSent time.Time
}

func (p *peer) handlePacket(hdr *hdr, pkt *pktbuff, private *ecdsa.PrivateKey, toUser chan *message, conn *net.UDPConn) error {
	switch hdr.kind {
	case typeClientHandshake:
		hs, e := handshakeLoad(pkt.head(int(hdr.len)), p.pubkey)
		if e != nil || hdr.crc32 != hs.crc32 {
			return newError("at client handshake", e)
		}
		key, e := p.epochs.new(int(hdr.epoch))
		if e != nil {
			return newError("creating new epoch", e)
		}
		if e := key.ecdh(hs.pubkey[:]); e != nil {
			return newError("shared secret", e)
		}

		p.ttlm = time.Now()
		if pkt.addr.String() != p.naddr.String() {
			p.naddr = pkt.addr
		}

		packet := allocPktbuff()
		packet.addr = p.naddr
		h := newHdr(typeServerHandshake, hdr.epoch, hdr.dst, hdr.src)
		h.len = handshakesz
		if e := h.dump(packet.tail(hdrsz)); e != nil {
			return newError("serializing hdr", e)
		}

		sh := &handshake{
			crc32: h.crc32,
		}
		copy(sh.pubkey[:], key.public())
		sh.crc32 = h.crc32
		if e := sh.dump(packet.tail(handshakesz), private); e != nil {
			return newError("serializing server handshake", e)
		}
		p.ready = true
		return packet.pktSend(conn)

	case typeServerHandshake:
		sh, e := handshakeLoad(pkt.head(int(hdr.len)), p.pubkey)
		if e != nil || hdr.crc32 != sh.crc32 {
			return newError("at server handshake", e)
		}

		pending, key := p.epochs.pending()
		if pending != int(hdr.epoch) {
			return newError("invalid epoch", nil)
		}
		if e := key.ecdh(sh.pubkey[:]); e != nil {
			return newError("shared secret", e)
		}
		// Promote
		if e := p.epochs.promote(pending); e != nil {
			return newError("impossible to promote new epoch", e)
		}
		// Enviar ctrlmessage

		p.ttlm = time.Now()
		p.ready = true
		if p.hndshk == true {
			p.hndshk = false
			p.hsSent = time.Time{}
			p.resend = nil
		}

		packet := allocPktbuff()
		packet.addr = p.naddr
		h := newHdr(typeCtrlMessage, hdr.epoch, hdr.dst, hdr.src)
		h.len = ctrlmessagesz
		if e := h.dump(packet.tail(hdrsz)); e != nil {
			return newError("serializing hdr", e)
		}
		ctrl := &ctrlmessage{}
		ctrl.crc32 = h.crc32
		ctrl.set(EpochAck)
		if e := ctrl.dump(packet.tail(ctrlmessagesz), private); e != nil {
			return newError("serializing ctrl message", e)
		}
		return packet.pktSend(conn)

	case typeCtrlMessage:
		c, e := ctrlmessageLoad(pkt.head(int(hdr.len)), p.pubkey)
		if e != nil || hdr.crc32 != c.crc32 {
			fmt.Println(hdr.String())
			fmt.Println(pkt)
			return newError("at ctrl message", e)
		}
		if c.isSet(EpochAck) {
			pending, _ := p.epochs.pending()
			if pending == int(hdr.epoch) {
				e := p.epochs.promote(pending)
				if e != nil {
					return newError("promoting new epoch", e)
				}
			}
		}
		p.ttlm = time.Now()
		if pkt.addr.String() != p.naddr.String() {
			p.naddr = pkt.addr
		}
		if c.isSet(KeepAlive) {
			packet := allocPktbuff()
			packet.addr = p.naddr
			header := newHdr(typeCtrlMessage, hdr.epoch, hdr.dst, hdr.src)
			header.len = ctrlmessagesz
			if e := header.dump(packet.tail(hdrsz)); e != nil {
				return newError("serializing hdr", e)
			}
			ctrl := &ctrlmessage{}
			ctrl.crc32 = header.crc32
			ctrl.set(KeepAliveAck)
			if e := ctrl.dump(packet.tail(ctrlmessagesz), private); e != nil {
				return newError("serializing ctrl message", e)
			}
			return packet.pktSend(conn)
		}
	case typeData:
		var (
			epoch int
			key   *dhss
		)
		epoch, key = p.epochs.current()
		if int(hdr.epoch) != epoch {
			if e := p.epochs.promote(int(hdr.epoch)); e != nil {
				return newError("invalid epoch", e)
			}
			epoch, key = p.epochs.current()
		}
		data, e := loadData(pkt.head(int(hdr.len)), key)
		if e != nil || data.crc32 != hdr.crc32 {
			return newError("at data reception", e)
		}
		p.ttlm = time.Now()
		if pkt.addr.String() != p.naddr.String() {
			p.naddr = pkt.addr
		}
		toUser <- &message{
			buff: data.buff,
			addr: hdr.src,
		}
	}
	return nil
}

func (p *peer) sendDataPacket(src uint16, buff []byte, conn *net.UDPConn) error {
	epoch, key := p.epochs.current()
	if epoch == -1 || key == nil {
		return newError("invalid epoch", nil)
	}
	packet := allocPktbuff()
	packet.addr = p.naddr
	hdr := newHdr(typeData, uint32(epoch), src, p.vaddr)
	hdr.len = uint16(len(buff) + dataOverload)
	if e := hdr.dump(packet.tail(hdrsz)); e != nil {
		return newError("hdr dump", e)
	}
	data := &data{}
	data.crc32 = hdr.crc32
	data.buff = buff
	if e := data.dump(key, packet.tail(int(hdr.len))); e != nil {
		return newError("data dump", e)
	}
	return packet.pktSend(conn)
}