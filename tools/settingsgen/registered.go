package main

import (
	_ "github.com/jeiang/atrium/internal/assistant"
	_ "github.com/jeiang/atrium/internal/assistant/budget"
	_ "github.com/jeiang/atrium/internal/assistant/loop"
	_ "github.com/jeiang/atrium/internal/assistant/scheduler"
	_ "github.com/jeiang/atrium/internal/auth/oidc"
	_ "github.com/jeiang/atrium/internal/calendar"
	_ "github.com/jeiang/atrium/internal/config"
	_ "github.com/jeiang/atrium/internal/contacts"
	_ "github.com/jeiang/atrium/internal/db/key"
	_ "github.com/jeiang/atrium/internal/mail"
	_ "github.com/jeiang/atrium/internal/media"
	_ "github.com/jeiang/atrium/internal/module"
	_ "github.com/jeiang/atrium/internal/money"
	_ "github.com/jeiang/atrium/internal/notify"
	_ "github.com/jeiang/atrium/internal/notion"
	_ "github.com/jeiang/atrium/internal/secrets"
)
