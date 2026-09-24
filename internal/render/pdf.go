package render

import (
	"context"
	"fmt"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// PrintHTML renders an HTML document to PDF. Page size and margins come from
// the document's CSS @page rules.
func (b *Browser) PrintHTML(parent context.Context, html string, timeout time.Duration) ([]byte, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	tabCtx, closeTab, err := b.newTab(ctx)
	if err != nil {
		return nil, err
	}
	defer closeTab()
	var pdf []byte
	err = chromedp.Run(tabCtx,
		chromedp.Navigate("about:blank"),
		chromedp.ActionFunc(func(ctx context.Context) error {
			tree, err := page.GetFrameTree().Do(ctx)
			if err != nil {
				return err
			}
			return page.SetDocumentContent(tree.Frame.ID, html).Do(ctx)
		}),
		// Margin boxes (page headers) cannot trigger font loading themselves,
		// so load every weight of the report font up front.
		chromedp.Evaluate(`Promise.all(['400','500','600','700'].map(w => document.fonts.load(w + ' 12px Inter'))).then(() => true)`, nil,
			func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }),
		// Wait for embedded images and fonts to decode.
		chromedp.Poll(`Array.from(document.images).every(i => i.complete) && (!document.fonts || document.fonts.status === 'loaded')`, nil,
			chromedp.WithPollingInterval(50*time.Millisecond), chromedp.WithPollingTimeout(20*time.Second)),
		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			pdf, _, err = page.PrintToPDF().
				WithPrintBackground(true).
				WithPreferCSSPageSize(true).
				WithMarginTop(0).WithMarginBottom(0).WithMarginLeft(0).WithMarginRight(0).
				WithGenerateDocumentOutline(true).
				WithGenerateTaggedPDF(true).
				Do(ctx)
			return err
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("print PDF: %w", err)
	}
	return pdf, nil
}
