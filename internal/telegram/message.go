package telegram

type OutgoingMessage struct {
	text      string
	parseMode string
}

func TextMessage(text string) OutgoingMessage {
	return OutgoingMessage{text: text}
}

func MarkdownMessage(text string) OutgoingMessage {
	return OutgoingMessage{text: text, parseMode: "Markdown"}
}

func (m OutgoingMessage) Text() string {
	return m.text
}

func (m OutgoingMessage) ParseMode() string {
	return m.parseMode
}
