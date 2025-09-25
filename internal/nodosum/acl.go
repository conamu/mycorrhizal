package nodosum

var godToken = token{
	token: "token",
	commands: map[int]bool{
		EXIT:  true,
		HELLO: true,
		ID:    true,
		GET:   true,
	},
}

var anonymousToken = token{
	token: "",
	commands: map[int]bool{
		EXIT:  true,
		HELLO: true,
		SET:   true,
	},
}

type token struct {
	token    string
	commands map[int]bool
}

func middleware(cmd int, token string) bool {

	if token == anonymousToken.token {
		if _, ok := anonymousToken.commands[cmd]; ok {
			return anonymousToken.commands[cmd]
		}
	}

	if token == godToken.token {
		if _, ok := godToken.commands[cmd]; ok {
			return godToken.commands[cmd]
		}
	}

	return false
}
