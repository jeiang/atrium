package imapscript

// Profile is a server's greeting and its capability lists before and after LOGIN.
type Profile struct {
	Greeting string
	Caps     string
	AuthCaps string
}

// Gmail is hand-written from Google's IMAP extension docs, not captured.
var Gmail = Profile{
	Greeting: "* OK Gimap ready for requests from 192.0.2.1 a1b2c3d4e5f6",
	Caps:     "IMAP4rev1 UNSELECT IDLE NAMESPACE QUOTA ID XLIST CHILDREN X-GM-EXT-1 XYZZY SASL-IR AUTH=XOAUTH2 AUTH=PLAIN AUTH=PLAIN-CLIENTTOKEN AUTH=OAUTHBEARER",
	AuthCaps: "IMAP4rev1 UNSELECT IDLE NAMESPACE QUOTA ID XLIST CHILDREN X-GM-EXT-1 UIDPLUS COMPRESS=DEFLATE ENABLE MOVE CONDSTORE ESEARCH UTF8=ACCEPT LIST-EXTENDED LIST-STATUS LITERAL- SPECIAL-USE APPENDLIMIT=35651584",
}

// ICloud is from a Thunderbird log of imap.mail.me.com (https://bugzilla.mozilla.org/show_bug.cgi?id=1388461).
// The greeting carries the capabilities, led by two server-id atoms.
var ICloud = Profile{
	Greeting: "* OK [CAPABILITY st11p00mm-iscream011 17E71 XAPPLEPUSHSERVICE IMAP4 IMAP4rev1 SASL-IR AUTH=ATOKEN AUTH=PLAIN] iSCREAM ready to rumble (17E71-26414-16A-2268-c90b5397169a:2479) st11p00mm-iscream011 [10:14837:05:45:08:05]",
	Caps:     "st11p00mm-iscream011 17E71 XAPPLEPUSHSERVICE IMAP4 IMAP4rev1 SASL-IR AUTH=ATOKEN AUTH=PLAIN",
	AuthCaps: "XAPPLEPUSHSERVICE IMAP4 IMAP4rev1 ACL RIGHTS=tekx QUOTA LITERAL+ NAMESPACE UIDPLUS CHILDREN BINARY UNSELECT SORT CATENATE URLAUTH LANGUAGE ESEARCH ESORT THREAD=ORDEREDSUBJECT THREAD=REFERENCES CONDSTORE ENABLE QRESYNC CONTEXT=SEARCH CONTEXT=SORT WITHIN SASL-IR SEARCHRES METADATA ID LIST-STATUS SPECIAL-USE CREATE-SPECIAL-USE MULTISEARCH SORT=DISPLAY LOGIN-REFERRALS X-SUN-SORT ANNOTATE-EXPERIMENT-1 X-SUN-IMAP XUM1 X-ORCL-AS IDLE XSNIPPET=FUZZY",
}

var vars = map[string]string{
	"gmail.greeting":  Gmail.Greeting,
	"gmail.caps":      Gmail.Caps,
	"gmail.authcaps":  Gmail.AuthCaps,
	"icloud.greeting": ICloud.Greeting,
	"icloud.caps":     ICloud.Caps,
	"icloud.authcaps": ICloud.AuthCaps,
}
