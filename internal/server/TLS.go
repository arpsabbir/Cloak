package server

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"

	log "github.com/sirupsen/logrus"
)

// ClientHello contains every field in a ClientHello message
type ClientHello struct {
	handshakeType         byte
	length                int
	clientVersion         []byte
	random                []byte
	sessionIdLen          int
	sessionId             []byte
	cipherSuitesLen       int
	cipherSuites          []byte
	compressionMethodsLen int
	compressionMethods    []byte
	extensionsLen         int
	extensions            map[[2]byte][]byte
}

var u16 = binary.BigEndian.Uint16
var u32 = binary.BigEndian.Uint32

func parseExtensions(input []byte) (ret map[[2]byte][]byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("Malformed Extensions")
		}
	}()
	pointer := 0
	totalLen := len(input)
	ret = make(map[[2]byte][]byte)
	for pointer < totalLen {
		var typ [2]byte
		copy(typ[:], input[pointer:pointer+2])
		pointer += 2
		length := int(u16(input[pointer : pointer+2]))
		pointer += 2
		data := input[pointer : pointer+length]
		pointer += length
		ret[typ] = data
	}
	return ret, err
}

func parseKeyShare(input []byte) (ret []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("malformed key_share")
		}
	}()
	totalLen := int(u16(input[0:2]))
	// 2 bytes "client key share length"
	pointer := 2
	for pointer < totalLen {
		if bytes.Equal([]byte{0x00, 0x1d}, input[pointer:pointer+2]) {
			// skip "key exchange length"
			pointer += 2
			length := int(u16(input[pointer : pointer+2]))
			pointer += 2
			if length != 32 {
				return nil, fmt.Errorf("key share length should be 32, instead of %v", length)
			}
			return input[pointer : pointer+length], nil
		}
		pointer += 2
		length := int(u16(input[pointer : pointer+2]))
		pointer += 2
		_ = input[pointer : pointer+length]
		pointer += length
	}
	return nil, errors.New("x25519 does not exist")
}

// addRecordLayer adds record layer to data
func addRecordLayer(input []byte, typ []byte, ver []byte) []byte {
	length := make([]byte, 2)
	binary.BigEndian.PutUint16(length, uint16(len(input)))
	ret := make([]byte, 5+len(input))
	copy(ret[0:1], typ)
	copy(ret[1:3], ver)
	copy(ret[3:5], length)
	copy(ret[5:], input)
	return ret
}

// parseClientHello parses everything on top of the TLS layer
// (including the record layer) into ClientHello type
func parseClientHello(data []byte) (ret *ClientHello, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("Malformed ClientHello")
		}
	}()

	peeled := make([]byte, len(data)-5)
	copy(peeled, data[5:])
	pointer := 0
	// Handshake Type
	handshakeType := peeled[pointer]
	if handshakeType != 0x01 {
		return ret, errors.New("Not a ClientHello")
	}
	pointer += 1
	// Length
	length := int(u32(append([]byte{0x00}, peeled[pointer:pointer+3]...)))
	pointer += 3
	if length != len(peeled[pointer:]) {
		return ret, errors.New("Hello length doesn't match")
	}
	// Client Version
	clientVersion := peeled[pointer : pointer+2]
	pointer += 2
	// Random
	random := peeled[pointer : pointer+32]
	pointer += 32
	// Session ID
	sessionIdLen := int(peeled[pointer])
	pointer += 1
	sessionId := peeled[pointer : pointer+sessionIdLen]
	pointer += sessionIdLen
	// Cipher Suites
	cipherSuitesLen := int(u16(peeled[pointer : pointer+2]))
	pointer += 2
	cipherSuites := peeled[pointer : pointer+cipherSuitesLen]
	pointer += cipherSuitesLen
	// Compression Methods
	compressionMethodsLen := int(peeled[pointer])
	pointer += 1
	compressionMethods := peeled[pointer : pointer+compressionMethodsLen]
	pointer += compressionMethodsLen
	// Extensions
	extensionsLen := int(u16(peeled[pointer : pointer+2]))
	pointer += 2
	extensions, err := parseExtensions(peeled[pointer:])
	ret = &ClientHello{
		handshakeType,
		length,
		clientVersion,
		random,
		sessionIdLen,
		sessionId,
		cipherSuitesLen,
		cipherSuites,
		compressionMethodsLen,
		compressionMethods,
		extensionsLen,
		extensions,
	}
	return
}

func xor(a []byte, b []byte) {
	for i := range a {
		a[i] ^= b[i]
	}
}

func composeServerHello(sessionId []byte, sharedSecret []byte, sessionKey []byte) []byte {
	var serverHello [11][]byte
	serverHello[0] = []byte{0x02}             // handshake type
	serverHello[1] = []byte{0x00, 0x00, 0x76} // length 77
	serverHello[2] = []byte{0x03, 0x03}       // server version
	xor(sharedSecret, sessionKey)
	serverHello[3] = sharedSecret       // random
	serverHello[4] = []byte{0x20}       // session id length 32
	serverHello[5] = sessionId          // session id
	serverHello[6] = []byte{0xc0, 0x30} // cipher suite TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384
	serverHello[7] = []byte{0x00}       // compression method null
	serverHello[8] = []byte{0x00, 0x2e} // extensions length 46

	keyShare, _ := hex.DecodeString("00330024001d0020")
	keyExchange := make([]byte, 32)
	rand.Read(keyExchange)
	serverHello[9] = append(keyShare, keyExchange...)

	serverHello[10], _ = hex.DecodeString("002b00020304")
	var ret []byte
	for _, s := range serverHello {
		ret = append(ret, s...)
	}
	return ret
}

// composeReply composes the ServerHello, ChangeCipherSpec and Finished messages
// together with their respective record layers into one byte slice. The content
// of these messages are random and useless for this plugin
func composeReply(ch *ClientHello, sharedSecret []byte, sessionKey []byte) []byte {
	TLS12 := []byte{0x03, 0x03}
	shBytes := addRecordLayer(composeServerHello(ch.sessionId, sharedSecret, sessionKey), []byte{0x16}, TLS12)
	ccsBytes := addRecordLayer([]byte{0x01}, []byte{0x14}, TLS12)
	ret := append(shBytes, ccsBytes...)
	return ret
}

var ErrBadClientHello = errors.New("non (or malformed) ClientHello")
var ErrNotCloak = errors.New("TLS but non-Cloak ClientHello")
var ErrBadProxyMethod = errors.New("invalid proxy method")

func PrepareConnection(firstPacket []byte, sta *State, conn net.Conn) (UID []byte, sessionID uint32, proxyMethod string, encryptionMethod byte, finisher func([]byte) error, err error) {
	ch, err := parseClientHello(firstPacket)
	if err != nil {
		log.Debug(err)
		err = ErrBadClientHello
		return
	}

	var sharedSecret []byte
	UID, sessionID, proxyMethod, encryptionMethod, sharedSecret, err = TouchStone(ch, sta)
	if err != nil {
		log.Debug(err)
		err = ErrNotCloak
		return
	}
	if _, ok := sta.ProxyBook[proxyMethod]; !ok {
		err = ErrBadProxyMethod
		return
	}

	finisher = func(sessionKey []byte) error {
		reply := composeReply(ch, sharedSecret, sessionKey)
		_, err = conn.Write(reply)
		if err != nil {
			go conn.Close()
			return err
		}
		return nil
	}
	return
}
