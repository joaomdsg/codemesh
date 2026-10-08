package page

import "encoding/json"

const script = `
one
two
three
`

var Schema = json.RawMessage(`{
	"type": "object"
}`)

const greeting = "hello, " +
	"world"

const Kind = "due"

func Query() string {
	return `
select 1
`
}

func Page() string { return script + greeting + Kind + string(Schema) }
