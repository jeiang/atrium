package communications

import (
	"regexp"
	"strings"
)

var (
	automatedLocal = regexp.MustCompile(`(?i)^(no[-_.]?reply|do[-_.]?not[-_.]?reply)([+._-].*)?$`)

	automatedTokens = map[string]bool{
		"account": true, "accounts": true, "admin": true, "administrator": true, "alert": true, "alerts": true,
		"auto": true, "automated": true, "billpay": true, "billing": true, "bounce": true, "bounces": true,
		"calendar": true, "care": true, "careers": true, "cards": true, "compliance": true, "confirmation": true,
		"contact": true, "customercare": true, "customerservice": true, "daemon": true, "deal": true, "deals": true,
		"delivery": true, "deliveries": true, "developer": true, "developers": true, "email": true, "event": true,
		"events": true, "feedback": true, "hello": true, "help": true, "helpdesk": true, "hostmaster": true,
		"info": true, "information": true, "invitation": true, "invitations": true, "invite": true, "invites": true,
		"invoice": true, "invoices": true, "jobs": true, "legal": true, "login": true, "mail": true, "mailer": true,
		"marketing": true, "media": true, "news": true, "newsletter": true, "newsletters": true, "notification": true,
		"notifications": true, "notify": true, "offer": true, "offers": true, "order": true, "orders": true,
		"outreach": true, "payment": true, "payments": true, "postmaster": true, "press": true, "privacy": true,
		"promo": true, "promotion": true, "promotions": true, "receipt": true, "receipts": true, "recruitment": true,
		"reply": true, "robot": true, "root": true, "sales": true, "security": true, "service": true, "services": true,
		"ship": true, "shipping": true, "shop": true, "signin": true, "statements": true, "store": true,
		"support": true, "survey": true, "surveys": true, "system": true, "team": true, "tracking": true,
		"update": true, "updates": true, "verification": true, "verify": true, "webmaster": true, "abuse": true,
		"bot": true,
	}

	automatedParts = []string{
		"billing", "invoice", "receipt", "order", "payment", "notification", "newsletter", "marketing",
		"promotion", "shipping", "delivery", "support", "service", "security", "survey", "alert", "update",
		"noreply", "donotreply", "mailer", "postmaster", "bounce", "customerservice", "customercare", "helpdesk",
		"webmaster", "hostmaster", "banking",
	}

	automatedDomainLabels = map[string]bool{
		"noreply": true, "no-reply": true, "notification": true, "notifications": true,
		"bounce": true, "bounces": true, "mailer": true, "postmaster": true,
	}
)

func AutomatedLocal(local string) (bool, error) {
	text := strings.ToLower(local)
	if automatedLocal.MatchString(text) {
		return true, nil
	}
	for _, token := range strings.FieldsFunc(text, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		if automatedTokens[token] {
			return true, nil
		}
	}
	for _, part := range automatedParts {
		if strings.Contains(text, part) {
			return true, nil
		}
	}
	return false, nil
}

func AutomatedDomain(domain string) bool {
	for _, label := range strings.Split(strings.ToLower(domain), ".") {
		if automatedDomainLabels[label] {
			return true
		}
	}
	return false
}
