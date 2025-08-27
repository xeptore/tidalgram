package telegram

import (
	"bytes"
	"fmt"

	"github.com/gotd/td/telegram/message"
	tghtml "github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/tg"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

func markdownToStyled(
	md string,
	userResolver func(id int64) (tg.InputUserClass, error),
) ([]message.StyledTextOption, error) {
	gm := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,           // tables, strikethrough, autolinks...
			extension.Strikethrough, // ensures ~~strike~~ becomes <s>
		),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
		),
		goldmark.WithRendererOptions(
			// IMPORTANT: Telegram expects plain tags; keep HTML minimal.
			html.WithHardWraps(), // respect line breaks
			html.WithXHTML(),     // harmless; td/html will re-escape as needed
		),
	)

	var buf bytes.Buffer
	if err := gm.Convert([]byte(md), &buf); nil != err {
		return nil, fmt.Errorf("failed to convert markdown to html: %v", err)
	}

	opt := tghtml.String(userResolver, buf.String())

	return []message.StyledTextOption{opt}, nil
}
