package imapclient

import "errors"

// xoauth2 is the SASL client for Google's XOAUTH2 mechanism, which go-sasl
// does not provide. The server expects one initial response and no challenge.
type xoauth2 struct {
	user  string
	token string
}

func (x *xoauth2) Start() (string, []byte, error) {
	return "XOAUTH2", []byte("user=" + x.user + "\x01auth=Bearer " + x.token + "\x01\x01"), nil
}

func (x *xoauth2) Next([]byte) ([]byte, error) {
	return nil, errors.New("XOAUTH2 does not expect a challenge")
}
