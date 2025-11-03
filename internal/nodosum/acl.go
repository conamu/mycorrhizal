package nodosum

/*
ACLs work with applications to enable secure controlflow.
Permission structure could be someting like
APP:METHOD:ACTION
ex.: CACHE:SET
ex.: CACHE:USER:READ
*/

var godToken = token{
	token:    "token",
	commands: map[int]bool{},
}

var anonymousToken = token{
	token:    "",
	commands: map[int]bool{},
}

type token struct {
	token    string
	commands map[int]bool
}

func middleware(cmd int, token string) bool {

	if public, ok := anonymousToken.commands[cmd]; ok && public {
		return true
	}

	if token == godToken.token {
		if _, ok := godToken.commands[cmd]; ok {
			return godToken.commands[cmd]
		}
	}

	return false
}
