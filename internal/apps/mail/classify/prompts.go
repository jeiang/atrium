package classify

import (
	"encoding/json"

	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/textutil"
)

const flagSystem = "You triage the mailbox owner's email and decide whether to flag it. Flag when a person (not an automated " +
	"sender) asks the owner something or needs their action, or there is a concrete deadline or payment due, or a " +
	"real fraud / sign-in / account-security event. Never flag statements, receipts, newsletters, promotions, or " +
	"routine notifications. Answer the JSON fields: person_asking (a human, not an automated sender, asks the owner " +
	"something or needs their action), deadline_or_payment_due (concrete deadline or payment due), security_event " +
	"(real fraud, sign-in or account-security event), and flag (final decision per the policy)."

var (
	flagSchema = json.RawMessage(`{"type":"object","properties":{"person_asking":{"type":"boolean"},` +
		`"deadline_or_payment_due":{"type":"boolean"},"security_event":{"type":"boolean"},"flag":{"type":"boolean"}},` +
		`"required":["person_asking","deadline_or_payment_due","security_event","flag"],"additionalProperties":false}`)
)

type Message struct{ Subject, FromName, FromAddr string }

func messagePrompt(m Message, body string) string {
	return "From: " + m.FromName + " <" + m.FromAddr + ">\nSubject: " + m.Subject + "\nBody:\n" + textutil.Clip(body, endpoint.TuningLlmBodyChars)
}
