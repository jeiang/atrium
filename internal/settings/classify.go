package settings

func ClassOf(key string) (ImportClass, bool) {
	entry, ok := ImportEntryFor(key)
	if !ok {
		return "", false
	}
	return entry.Class, true
}

func CutoverEntry(key string) (ImportEntry, bool) {
	entry, ok := ImportEntryFor(key)
	if !ok || !entry.Runtime {
		return ImportEntry{}, false
	}
	return entry, true
}

func CutoverDestination(entry ImportEntry) string {
	return Destination(entry)
}

func LocalChatKey(key string) bool {
	switch key {
	case "mail_chat_url", "mail_chat_model", "mail_chat_ca_file", "mail_chat_proxy", "mail_chat_key":
		return true
	}
	return false
}

func HostOwnedKey(key string) bool {
	if LocalChatKey(key) {
		return false
	}
	entry, ok := ImportEntryFor(key)
	return ok && entry.Class == ImportHost
}

func DBBacked(key string) bool {
	if LocalChatKey(key) {
		return true
	}
	class, ok := ClassOf(key)
	return !ok || class == ImportOperator
}

func hostOwned(key string) error {
	entry, _ := ImportEntryFor(key)
	return HostOwned{Key: key, Destination: entry.Target}
}
