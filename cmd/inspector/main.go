package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"ExpeditusClient/internal/browser"

	"github.com/chromedp/chromedp"
)

type PageAnalysis struct {
	URL             string   `json:"url"`
	Title           string   `json:"title"`
	IsSPA           bool     `json:"is_spa"`
	MetaDescription string   `json:"meta_description"`
	SemanticAnchors []string `json:"semantic_anchors"`
	FormsFound      int      `json:"forms_count"`
	ButtonsFound    int      `json:"buttons_count"`
	SuggestedDriver string   `json:"suggested_driver"`
	Error           string   `json:"error,omitempty"`
}

func main() {
	urlFlag := flag.String("url", "", "URL to inspect")
	timeoutFlag := flag.Int("timeout", 30, "Timeout in seconds")
	waitSelector := flag.String("wait", "", "CSS selector to wait for")
	flag.Parse()

	if *urlFlag == "" {
		fail("URL is required. Usage: -url <https://example.com>")
	}

	cfg := browser.DefaultConfig()
	cfg.Timeout = time.Duration(*timeoutFlag) * time.Second

	ctx := context.Background()
	pool, err := browser.NewPool(ctx, cfg)
	if err != nil {
		fail(fmt.Sprintf("browser pool error: %v", err))
	}
	defer pool.Close()

	analysis, err := inspectPage(ctx, pool, *urlFlag, *waitSelector)
	if err != nil {
		fail(err.Error())
	}

	outputJSON(analysis)
}

func inspectPage(ctx context.Context, pool *browser.Pool, url, waitSelector string) (*PageAnalysis, error) {
	browserCtx, cancel := pool.NewContext(ctx)
	defer cancel()

	var raw map[string]interface{}

	tasks := []chromedp.Action{
		chromedp.Navigate(url),
		chromedp.WaitReady("body", chromedp.ByQuery),
	}

	if waitSelector != "" {
		tasks = append(tasks, chromedp.WaitVisible(waitSelector, chromedp.ByQuery))
	}

	tasks = append(tasks, chromedp.Evaluate(buildInspectionScript(), &raw))

	if err := chromedp.Run(browserCtx, tasks...); err != nil {
		return nil, fmt.Errorf("inspection failed: %w", err)
	}

	return parseAnalysis(url, raw), nil
}

func buildInspectionScript() string {
	return `(() => {
		const isSPA = !!(
			document.querySelector('#root') ||
			document.querySelector('#app') ||
			document.querySelector('[data-reactroot]') ||
			(document.scripts.length > 5 && document.body.innerText.length < 500)
		);

		const metaDesc = document.querySelector('meta[name="description"]')?.content || "";

		const getVisibleText = (node) => {
			if (node.nodeType === Node.TEXT_NODE) {
				const text = node.textContent.trim();
				if (text.length >= 3 && text.length <= 40 && isNaN(Number(text)) && !text.includes('{')) {
					return text;
				}
			}
			return null;
		};

		const anchors = new Set();
		const walker = document.createTreeWalker(
			document.body,
			NodeFilter.SHOW_TEXT,
			{ acceptNode: (node) => {
				if (['SCRIPT', 'STYLE', 'NOSCRIPT'].includes(node.parentNode.nodeName)) {
					return NodeFilter.FILTER_REJECT;
				}
				if (node.parentNode.offsetParent === null) {
					return NodeFilter.FILTER_REJECT;
				}
				return NodeFilter.FILTER_ACCEPT;
			}}
		);

		let count = 0;
		while(walker.nextNode() && count < 50) {
			const txt = getVisibleText(walker.currentNode);
			if (txt) {
				anchors.add(txt);
				count++;
			}
		}

		return {
			title: document.title,
			is_spa: isSPA,
			meta_description: metaDesc,
			semantic_anchors: Array.from(anchors),
			forms_count: document.querySelectorAll('form').length,
			buttons_count: document.querySelectorAll('button, [role="button"], input[type="submit"]').length
		};
	})()`
}

func parseAnalysis(url string, raw map[string]interface{}) *PageAnalysis {
	analysis := &PageAnalysis{
		URL:             url,
		Title:           getStringSafe(raw, "title"),
		MetaDescription: getStringSafe(raw, "meta_description"),
		IsSPA:           getBoolSafe(raw, "is_spa"),
		FormsFound:      getIntSafe(raw, "forms_count"),
		ButtonsFound:    getIntSafe(raw, "buttons_count"),
	}

	if anchors, ok := raw["semantic_anchors"].([]interface{}); ok {
		analysis.SemanticAnchors = make([]string, 0, len(anchors))
		for _, a := range anchors {
			if s, ok := a.(string); ok {
				analysis.SemanticAnchors = append(analysis.SemanticAnchors, s)
			}
		}
	}

	if analysis.IsSPA || analysis.ButtonsFound > 10 {
		analysis.SuggestedDriver = "chromedp"
	} else {
		analysis.SuggestedDriver = "sonar-static"
	}

	return analysis
}

func getStringSafe(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getBoolSafe(m map[string]interface{}, key string) bool {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func getIntSafe(m map[string]interface{}, key string) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return 0
}

func fail(msg string) {
	errObj := PageAnalysis{Error: msg}
	data, _ := json.Marshal(errObj)
	fmt.Println(string(data))
	os.Exit(1)
}

func outputJSON(a *PageAnalysis) {
	data, _ := json.MarshalIndent(a, "", "  ")
	fmt.Println(string(data))
}
