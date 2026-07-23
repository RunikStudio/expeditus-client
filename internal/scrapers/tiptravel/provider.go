// Package tiptravel implements a scraper for tiptravelya.com, a
// Travel Compositor (TR2) microsite by TravelcOnline. The site uses
// JavaServer Faces + PrimeFaces, so the login flow is a POST to "/"
// with PrimeFaces-style form fields.
package tiptravel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ExpeditusClient/internal/api"
	"ExpeditusClient/internal/config"
	"ExpeditusClient/internal/scrapers"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

const (
	Name             = scrapers.ProviderTipTravel
	defaultTimeout   = 300 * time.Second
	defaultLoginURL  = "https://www.tiptravelya.com/home"
	micrositeID      = "tiptravel"
	passwordSel      = `input[name$="login:j_password"]`
	emailSel         = `input[name$="login:Email"]`
	signinSel        = `button[id$="login:signin"]`
	searchCompleteJS = `document.body.innerText.includes('Búsqueda completada') || document.body.innerText.includes('resultados') || document.querySelectorAll('.hotel-card, .accommodation-card, [class*="result"]').length > 0`
)

// Config holds tiptravel-specific credentials. Username is optional; the
// .env in the API uses an unfortunate name "userTip" that actually
// contains the URL, so we accept either TIP_USER or TIP_USERNAME and
// default to "matriz" which matches the password style "matriz-TTY1".
type Config struct {
	LoginURL string
	Username string
	Password string
}

// LoadConfig reads tiptravel credentials from environment. Recognized
// variables (priority order):
//
//	username : userTip / TIP_USER / TIP_USERNAME / TIPTRAVEL_USER
//	           (default "matriz")
//	password : passTip / TIP_PASSWORD / TIP_PASS / TIPTRAVEL_PASSWORD
//	loginUrl : TIP_URL / TIPTRAVEL_URL  (default https://www.tiptravelya.com/)
//
// Note: the ExpeditusApi/.env uses the name "userTip" for the username
// even though the name reads like a URL. We treat userTip as the
// username (email) and never as a URL.
func LoadConfig() *Config {
	username := firstNonEmpty(
		"userTip",
		"TIP_USER",
		"TIP_USERNAME",
		"TIPTRAVEL_USER",
	)
	if username == "" {
		username = "matriz"
	}
	loginURL := firstNonEmpty(
		"TIP_URL",
		"TIPTRAVEL_URL",
		"tipTravelUrl", // explicit, not userTip
	)
	if loginURL == "" {
		loginURL = defaultLoginURL
	}
	password := firstNonEmpty(
		"passTip",
		"TIP_PASSWORD",
		"TIP_PASS",
		"TIPTRAVEL_PASSWORD",
	)
	return &Config{
		LoginURL: loginURL,
		Username: username,
		Password: password,
	}
}

func firstNonEmpty(keys ...string) string {
	for _, k := range keys {
		if v := getEnv(k); v != "" {
			return v
		}
	}
	return ""
}

func getEnv(k string) string {
	if v, ok := lookupEnv(k); ok {
		return v
	}
	return ""
}

// lookupEnv is overridable for tests; defaults to os.LookupEnv.
var lookupEnv = defaultLookupEnv

// Register adds the tiptravel provider to the scrapers registry. Must
// be called once at startup (the binary does this from
// cmd/expeditus-web).
func Register() error {
	tcfg := LoadConfig()
	if tcfg.Password == "" {
		return errors.New("tiptravel: missing password (set TIP_PASSWORD or passTip)")
	}
	scrapers.Register(Name, func(cfg *config.LoginConfig) (scrapers.ScraperProvider, error) {
		return &Provider{cfg: LoadConfig()}, nil
	})
	return nil
}

// Provider implements ScraperProvider for tiptravelya.com.
type Provider struct {
	cfg *Config
}

// New returns a configured Provider. Convenience constructor for callers
// that bypass the registry.
func New(cfg *Config) *Provider {
	if cfg == nil {
		cfg = LoadConfig()
	}
	return &Provider{cfg: cfg}
}

// Name returns "tiptravel".
func (p *Provider) Name() string { return Name }

// LoginURL returns the entry point URL.
func (p *Provider) LoginURL() string {
	if p.cfg != nil && p.cfg.LoginURL != "" {
		return p.cfg.LoginURL
	}
	return defaultLoginURL
}

// Run executes the tiptravel scraping flow: login via HTTP, then
// interactive hotel search via chromedp, then price extraction.
//
// Why mixed HTTP + chromedp:
//   - Login is done over plain HTTP because PrimeFaces/JSF on Travel
//     Compositor silently rejects AJAX login attempts from automation
//     clients (200 + empty growl, no USER_ID cookie).
//   - The hotel search form uses PrimeFaces autocomplete which requires
//     a JS runtime, so we hand the session cookies to chromedp and
//     drive the form from there.
func (p *Provider) Run(ctx context.Context, session *scrapers.ScraperSession, order *api.ScrapingOrder) error {
	if session == nil {
		return errors.New("tiptravel: nil session")
	}
	if session.Reporter == nil {
		return errors.New("tiptravel: nil reporter")
	}
	if p.cfg.Password == "" {
		return errors.New("tiptravel: missing password")
	}

	session.Reporter.Progress(5, "login", "Login via HTTP...")
	httpClient, err := p.httpLoginClient(ctx)
	if err != nil {
		return fmt.Errorf("tiptravel: http login failed: %w", err)
	}
	authCookies := httpClient.Jar.Cookies(mustParseURL("https://www.tiptravelya.com/home"))
	log.Printf("tiptravel: HTTP login OK, %d cookies", len(authCookies))

	browserCtx, cancel := session.NewBrowser(session.Ctx)
	defer cancel()

	if err := p.injectCookiesAsHostOnly(browserCtx, authCookies); err != nil {
		return fmt.Errorf("tiptravel: inject cookies: %w", err)
	}

	session.Reporter.Progress(15, "login", "Verificando sesión en browser...")
	if err := chromedp.Run(browserCtx,
		chromedp.Navigate(p.LoginURL()),
		chromedp.WaitReady("body"),
		chromedp.Sleep(2*time.Second),
	); err != nil {
		return fmt.Errorf("tiptravel: post-login navigation: %w", err)
	}
	var hasLogout, hasUserName bool
	var pageURL string
	var allCookies string
	var bodyText string
	diagActions := []chromedp.Action{
		chromedp.Location(&pageURL),
		chromedp.Evaluate(`document.cookie`, &allCookies),
		chromedp.Evaluate(`(document.body.innerText || '').includes('Juan Lucas')`, &hasUserName),
		chromedp.Evaluate(`(document.body.innerText || '').includes('Cerrar')`, &hasLogout),
	}
	if err := chromedp.Run(browserCtx, diagActions...); err != nil {
		return fmt.Errorf("tiptravel: post-login state check: %w", err)
	}
	log.Printf("tiptravel: after navigation pageURL=%q hasUserName=%v hasLogout=%v", pageURL, hasUserName, hasLogout)
	if !hasUserName && !hasLogout {
		chromedp.Run(browserCtx, chromedp.Evaluate(`document.body.innerText.substring(0, 500)`, &bodyText))
		log.Printf("tiptravel: body snippet: %q", bodyText)
		return errors.New("tiptravel: post-login state shows no 'Cerrar sesion' - session cookie transfer failed")
	}
	log.Printf("tiptravel: session is live in browser")

	if order == nil {
		return errors.New("tiptravel: nil order")
	}

	session.Reporter.Progress(25, "search", "Buscando hotel...")
	if err := p.runHotelSearch(browserCtx, order); err != nil {
		return fmt.Errorf("tiptravel: hotel search: %w", err)
	}

	session.Reporter.Progress(80, "scraping", "Esperando resultados...")
	if err := p.waitForResults(browserCtx); err != nil {
		log.Printf("tiptravel: waitForResults warning: %v", err)
	}

	// Debug: inspect card structure to detect package vs hotel-only indicators
	var cardDebug string
	chromedp.Run(browserCtx, chromedp.Evaluate(`(function(){
		var cards=document.querySelectorAll('.c-card');
		var results=[];
		for(var i=0;i<Math.min(cards.length,5);i++){
			var c=cards[i];
			var priceEl=c.querySelector('b[class*="price"]');
			var text=priceEl?(priceEl.innerText||'').trim():'NO_PRICE';
			var fullHTML=c.innerHTML.slice(0,1200);
			var classes=c.className;
			// Check for package indicators WITHIN the card
			var hasVueloCard=/vuelo|flight|avion/i.test(fullHTML);
			var hasPaqueteCard=/paquete|package/i.test(fullHTML);
			var hasPromoCard=/promo|descuento|oferta/i.test(fullHTML);
			var dataPkg=c.dataset.packageType||'';
			// Check card origin class
			var isHotelOnly=/origin--hotel/i.test(classes);
			var isOriginUnique=/origin--unique/i.test(classes);
			// Look for SVG icons (flight = airplane)
			var hasAirplaneSVG=/airplane|plane|fly|volar/i.test(fullHTML.slice(0,2000));
			// Where is "vuelo" appearing?
			var vueloIdx=fullHTML.toLowerCase().indexOf('vuelo');
			var vueloChunk=vueloIdx>=0?fullHTML.slice(Math.max(0,vueloIdx-30),vueloIdx+60):'';
			results.push({idx:i,price:text,classes:classes.trim(),isHotelOnly:isHotelOnly,isOriginUnique:isOriginUnique,hasVueloCard:hasVueloCard,hasPaqueteCard:hasPaqueteCard,hasPromoCard:hasPromoCard,dataPkg:dataPkg,hasAirplaneSVG:hasAirplaneSVG,vueloChunk:vueloChunk});
		}
		// Also look for hotel-only sections
		var hotelOnlyCards=document.querySelectorAll('.c-card--idea__origin--hotel,.c-card--origin--hotel,.c-hotel-card,[class*="origin--hotel"]');
		var allClasses=[];
		var sampleCard=document.querySelector('.c-card');
		if(sampleCard)allClasses=sampleCard.className.split(' ').filter(Boolean);
		// Check form inputs - look for ALL hidden and visible inputs
		var allInputs=[];
		var inputs=document.querySelectorAll('input');
		for(var j=0;j<inputs.length;j++){
			var inp=inputs[j];
			allInputs.push({id:inp.id,name:inp.name,value:inp.value.substring(0,50),type:inp.type});
		}
		// Get the page URL
		var pageURL=window.location.href;
		// Look for form submit URL
		var form=document.querySelector('form[id$="form"],form[id*="search"]');
		var formAction=form?form.action||'no-action':'no-form';
		// Look for select elements (tripType, accommodation type)
		var allSelects=[];
		var selects=document.querySelectorAll('select');
		for(var s=0;s<selects.length;s++){
			var sel=selects[s];
			var opts=[];
			var selectedOpts=sel.selectedOptions;
			for(var o=0;o<sel.options.length;o++){
				opts.push({value:sel.options[o].value,text:sel.options[o].text,selected:sel.options[o].selected});
			}
			allSelects.push({id:sel.id,name:sel.name,options:opts});
		}
		return JSON.stringify({totalCards:cards.length,hotelOnlyCount:hotelOnlyCards.length,allClasses:allClasses,firstCardHTML:cards[0]?cards[0].innerHTML.slice(0,500):'',pageURL:pageURL,formAction:formAction,allInputs:allInputs.slice(0,30),allSelects:allSelects.slice(0,10)});
	})()`, &cardDebug))
	log.Printf("tiptravel: card debug: %s", cardDebug)

	session.Reporter.Progress(95, "scraping", "Extrayendo precios...")
	prices, err := p.extractPricesFromBrowser(browserCtx, order)
	if err != nil {
		return fmt.Errorf("tiptravel: price extraction: %w", err)
	}

	session.Reporter.Progress(100, "complete", fmt.Sprintf("OK - %d resultados", len(prices)))
	for id, data := range prices {
		session.Reporter.Result(id, data)
	}
	return nil
}

// injectCookiesAsHostOnly transfers cookies from an HTTP login session
// to a chromedp browser context. Uses the `url` parameter of CDP's
// Network.setCookie so Chrome treats them as host-only cookies (matching
// what Go's cookiejar returns when domain is empty). Domain cookies
// get rejected by Chrome for some servers when the source jar had
// empty domains.
func (p *Provider) injectCookiesAsHostOnly(ctx context.Context, cookies []*http.Cookie) error {
	return chromedp.Run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		exp := cdp.TimeSinceEpoch(time.Now().Add(24 * time.Hour))
		const origin = "https://www.tiptravelya.com"
		seen := map[string]bool{}
		for _, ck := range cookies {
			if ck.Name == "" || seen[ck.Name] {
				continue
			}
			seen[ck.Name] = true
			if err := network.SetCookie(ck.Name, ck.Value).
				WithURL(origin).
				WithPath(orDefault(ck.Path, "/")).
				WithExpires(&exp).
				Do(c); err != nil {
				return fmt.Errorf("set %s: %w", ck.Name, err)
			}
			log.Printf("tiptravel: injected cookie %s=%s", ck.Name, truncate(ck.Value, 24))
		}
		return nil
	}))
}

// runHotelSearch drives the search form in the browser: type the
// hotel name, select from autocomplete, fill check-in/check-out dates
// and (if the order has a distribution string) configure the rooms +
// passengers popup.
func (p *Provider) runHotelSearch(ctx context.Context, order *api.ScrapingOrder) error {
	hotelName := order.HotelName
	if hotelName == "" {
		// Fallback: use hotel_id as search query (Travel Compositor
		// supports both hotel names and internal ids).
		hotelName = order.HotelID
	}
	if hotelName == "" {
		return errors.New("tiptravel: order has neither HotelName nor HotelID")
	}

	destSel := `input[id$="destinationOnlyAccommodation_input"]`
	depSel := `input[id$="departureOnlyAccommodation:input"]`
	arrSel := `input[id$="arrivalOnlyAccommodation:input"]`

	// 1) Type hotel name into the destination autocomplete.
	if err := chromedp.Run(ctx,
		chromedp.Click(destSel, chromedp.NodeVisible),
		chromedp.Sleep(300*time.Millisecond),
		chromedp.SendKeys(destSel, hotelName, chromedp.ByQuery),
		chromedp.Sleep(2*time.Second), // PrimeFaces debounce on autocomplete
	); err != nil {
		return fmt.Errorf("typing destination: %w", err)
	}

	// 2) Wait for PrimeFaces autocomplete panel and pick the first
	//    entry that matches the order's hotel name (or hotel id) or
	//    fall back to the first entry. The panel is appended to body
	//    by PrimeFaces with .ui-autocomplete-panel class.
	var picked string
	if err := chromedp.Run(ctx,
		chromedp.ActionFunc(func(c context.Context) error {
			panelSel := `.ui-autocomplete-panel li.ui-autocomplete-item, .ui-autocomplete-panel .ui-autocomplete-item`
			var rawJSON string
			for i := 0; i < 20; i++ {
				if err := chromedp.Evaluate(`(() => {
					const items = document.querySelectorAll('.ui-autocomplete-panel .ui-autocomplete-item, .ui-autocomplete-panel li');
					return JSON.stringify(Array.from(items).map(e => e.innerText.trim().slice(0, 200)));
				})()`, &rawJSON).Do(c); err != nil {
					return err
				}
				if rawJSON != "" && rawJSON != "null" {
					break
				}
				time.Sleep(250 * time.Millisecond)
			}
			if rawJSON == "" || rawJSON == "null" {
				return errors.New("autocomplete panel did not populate")
			}
			var items []string
			if err := json.Unmarshal([]byte(rawJSON), &items); err != nil {
				return fmt.Errorf("autocomplete JSON parse: %w (raw=%q)", err, truncate(rawJSON, 100))
			}
			if len(items) == 0 {
				return errors.New("autocomplete panel returned 0 items")
			}
			log.Printf("tiptravel: autocomplete returned %d items", len(items))
			for _, it := range items {
				if order.HotelName != "" && strings.Contains(strings.ToLower(it), strings.ToLower(order.HotelName)) {
					picked = it
					break
				}
			}
			if picked == "" {
				picked = items[0]
			}
			safePicked := strings.ReplaceAll(picked, `'`, `\\'`)
			clickJS := fmt.Sprintf(`(function(){var items=document.querySelectorAll('%s');for(var i=0;i<items.length;i++){if(items[i].innerText.trim()===('%s')){items[i].click();return;}}})()`, strings.ReplaceAll(panelSel, `'`, `\\'`), safePicked)
			return chromedp.Evaluate(clickJS, nil).Do(c)
		}),
	); err != nil {
		return fmt.Errorf("selecting hotel from autocomplete: %w (picked=%q)", err, picked)
	}
	if err := chromedp.Run(ctx, chromedp.Sleep(1*time.Second)); err != nil {
		return err
	}

	// 3) Fill dates if provided. PrimeFaces calendar inputs expect
	// dd/MM/yyyy. Order.CheckIn/CheckOut come from the API in the
	// same format ("23/03/2026") so we can pass them through.
	if order.CheckIn != "" {
		if err := p.fillDateInput(ctx, depSel, order.CheckIn); err != nil {
			log.Printf("tiptravel: warning filling check-in: %v", err)
		}
	}
	if order.CheckOut != "" {
		if err := p.fillDateInput(ctx, arrSel, order.CheckOut); err != nil {
			log.Printf("tiptravel: warning filling check-out: %v", err)
		}
	}


	// 4) Instead of clicking "Buscar" (which submits via AJAX without
	//    tripType), extract form values and navigate directly to a search
	//    URL with explicit tripType=ONLY_HOTEL. This mirrors the original
	//    HTTP e2e approach that correctly returned hotel-only results.
	//    Distribution is encoded in the URL (not via UI).

	var rawForm string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(function(){
		var hid=document.querySelector('input[id$="destinationOnlyAccommodation_hinput"]');
		var dep=document.querySelector('input[id$="departureOnlyAccommodation:input"]');
		var arr=document.querySelector('input[id$="arrivalOnlyAccommodation:input"]');
		return JSON.stringify({
			hotelDest:hid?hid.value:'NOT_FOUND',
			checkIn:dep?dep.value:'NOT_FOUND',
			checkOut:arr?arr.value:'NOT_FOUND'
		});
	})()`, &rawForm)); err != nil {
		return fmt.Errorf("extracting form values: %w", err)
	}
	var formState struct {
		HotelDest string `json:"hotelDest"`
		CheckIn   string `json:"checkIn"`
		CheckOut  string `json:"checkOut"`
	}
	if err := json.Unmarshal([]byte(rawForm), &formState); err != nil {
		return fmt.Errorf("parsing form state JSON: %w (raw=%q)", err, truncate(rawForm, 100))
	}

	// Build distribution string for URL (e.g. "2A" -> "2~~0~~",
	// "1A+1C5" -> "1~~0~~::1~~1~~5")
	rooms := parseDistribution(orDefault(order.Passengers, "2A"))
	dist := buildDistributionURL(rooms)

	checkIn := orDefault(order.CheckIn, formState.CheckIn)
	checkOut := orDefault(order.CheckOut, formState.CheckOut)
	hotelDest := formState.HotelDest
	if hotelDest == "" || hotelDest == "NOT_FOUND" {
		hotelDest = "Hotel::" + orDefault(order.HotelID, "UNKNOWN")
	}

	searchURL := fmt.Sprintf("https://www.tiptravelya.com/home?"+
		"directSubmit=true&latestSearch=true&tripType=ONLY_HOTEL"+
		"&distribution=%s&departureDate=%s&arrivalDate=%s&hotelDestination=%s",
		url.QueryEscape(dist),
		url.QueryEscape(checkIn),
		url.QueryEscape(checkOut),
		url.QueryEscape(hotelDest))
	log.Printf("tiptravel: navigating to search URL (tripType=ONLY_HOTEL)")

	if err := chromedp.Run(ctx,
		chromedp.Navigate(searchURL),
		chromedp.WaitReady("body"),
		chromedp.Sleep(3*time.Second),
	); err != nil {
		return fmt.Errorf("navigating to search URL: %w", err)
	}

	var resultInfo string
	chromedp.Run(ctx, chromedp.Evaluate(`(function(){
		var cards=document.querySelectorAll('.c-card');
		var first=document.querySelector('.c-card');
		var cls=first?first.className:'none';
		var hotelOnly=/origin--hotel/i.test(cls);
		var unique=/origin--unique/i.test(cls);
		var flight=/vuelo|flight/i.test(first?first.innerHTML:'');
		return JSON.stringify({total:cards.length,hasHotelOnly:hotelOnly,hasUnique:unique,hasFlight:flight,firstClass:cls.slice(0,100)});
	})()`, &resultInfo))
	log.Printf("tiptravel: result type: %s", resultInfo)
	return nil
}

// fillDateInput fills a PrimeFaces calendar input field. PrimeFaces
// calendar uses hidden inputs for the actual value - we set both the
// visible input and the PrimeFaces widget value via JS events.
func (p *Provider) fillDateInput(ctx context.Context, sel, value string) error {
	fillJS := fmt.Sprintf(`(function(sel,val){
		var el=document.querySelector(sel);
		if(!el)return'not_found:'+sel;
		// Clear and set visible input
		el.value=val;
		// Fire native input event for PrimeFaces to catch
		el.dispatchEvent(new Event('input',{bubbles:true}));
		el.dispatchEvent(new Event('change',{bubbles:true}));
		// Press Enter to close calendar picker if open
		el.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',keyCode:13,bubbles:true}));
		return'filled:'+val;
	})('%s','%s')`, strings.ReplaceAll(sel, `'`, `\\'`), value)
	var result string
	return chromedp.Run(ctx,
		chromedp.ActionFunc(func(c context.Context) error {
			return chromedp.Evaluate(fillJS, &result).Do(c)
		}),
		chromedp.Sleep(500*time.Millisecond),
	)
}

// applyDistribution opens the rooms/passengers popup and configures
// it according to the order's distribution string. The popup is opened
// by clicking the "Seleccionar huespedes" / "1 Habitacion, 2 Adultos"
// label, which renders a panel with room rows and "+/-" buttons per
// adult/child slot.
//
// The order.Passengers format is "RA+SAGE0-AGE", e.g.:
//   "2A"             -> 2 adults, no children
//   "1A+1C5"         -> 1 adult + 1 child age 5
//   "2A+2C3+C7"      -> 2 adults + 2 children (ages 3, 7)
//   "1A+1C0-2"       -> 1 adult + 1 child (age 0-2 bracket)
func (p *Provider) applyDistribution(ctx context.Context, dist string) error {
	rooms := parseDistribution(dist)
	if len(rooms) == 0 {
		return fmt.Errorf("unparseable distribution: %q", dist)
	}
	log.Printf("tiptravel: applying distribution: %+v", rooms)

	// 1) Click the passengers summary to open the popup.
	if err := chromedp.Run(ctx,
		chromedp.ActionFunc(func(c context.Context) error {
			return chromedp.Evaluate(`(() => {
				const labels = document.querySelectorAll('.c-searchbox__distribution, .dev-distribution-selector, [class*="distribution" i]');
				for (const el of labels) {
					const r = el.getBoundingClientRect();
					if (r.width > 0 && r.height > 0) {
						el.click();
						return 'clicked';
					}
				}
				return 'not_found';
			})()`, nil).Do(c)
		}),
		chromedp.Sleep(500*time.Millisecond),
	); err != nil {
		return err
	}

	// 2) For each room, increment adult/child counters. The exact
	//    button selectors depend on PrimeFaces id generation; we use
	//    data attributes when available and fall back to text/icon.
	for ri, room := range rooms {
		// Set adults: click "+" until count matches.
		for ai := 0; ai < room.Adults-1; ai++ {
			if err := clickByLabel(ctx, "Adulto", "más", ri); err != nil {
				log.Printf("tiptravel: warning adding adult %d in room %d: %v", ai+2, ri, err)
			}
		}
		for ci := 0; ci < room.Children; ci++ {
			if err := clickByLabel(ctx, "Niño", "más", ri); err != nil {
				log.Printf("tiptravel: warning adding child %d in room %d: %v", ci+1, ri, err)
			}
		}
		// Set child ages via select dropdowns.
		for ci, age := range room.ChildAges {
			if err := selectChildAge(ctx, ci, age); err != nil {
				log.Printf("tiptravel: warning setting child %d age to %d: %v", ci, age, err)
			}
		}
	}

	// 3) Click "Aceptar".
	if err := chromedp.Run(ctx,
		chromedp.ActionFunc(func(c context.Context) error {
			return chromedp.Evaluate(`document.querySelector('a.accept-distributions, button.accept-distributions')?.click()`, nil).Do(c)
		}),
		chromedp.Sleep(500*time.Millisecond),
	); err != nil {
		return err
	}
	return nil
}

func clickByLabel(ctx context.Context, label, op string, roomIdx int) error {
	var result string
	script := fmt.Sprintf(`(() => {
		const rooms = document.querySelectorAll('.dev-distribution-room, .c-searchbox__distribution__room');
		const room = rooms[%d];
		if (!room) return 'no_room';
		const buttons = room.querySelectorAll('button, .c-searchbox__distribution__btn, [class*="more" i], [class*="plus" i]');
		for (const b of buttons) {
			const txt = (b.textContent || b.getAttribute('aria-label') || '').toLowerCase();
			if (txt.includes('%s'.toLowerCase()) || txt.includes('+')) {
				b.click();
				return 'clicked';
			}
		}
		return 'no_btn';
	})()`, roomIdx, label)
	return chromedp.Run(ctx,
		chromedp.ActionFunc(func(c context.Context) error {
			return chromedp.Evaluate(script, &result).Do(c)
		}),
		chromedp.Sleep(150*time.Millisecond),
	)
}

func selectChildAge(ctx context.Context, childIdx int, age int) error {
	var result string
	script := fmt.Sprintf(`(() => {
		const selects = document.querySelectorAll('.dev-distribution-room select, .c-searchbox__distribution__room select');
		if (selects[%d]) {
			selects[%d].value = String(%d);
			selects[%d].dispatchEvent(new Event('change', { bubbles: true }));
			return 'ok';
		}
		return 'no_select';
	})()`, childIdx, childIdx, age, childIdx)
	return chromedp.Run(ctx,
		chromedp.ActionFunc(func(c context.Context) error {
			return chromedp.Evaluate(script, &result).Do(c)
		}),
	)
}

// waitForResults waits until at least one price card appears or the
// search-complete indicator is set.
func (p *Provider) waitForResults(ctx context.Context) error {
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		var found bool
		_ = chromedp.Run(ctx,
			chromedp.Evaluate(`document.querySelectorAll('.c-card__idea-price__primary, .c-hotel-card, [class*="result" i]').length > 0`, &found),
		)
		if found {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return errors.New("results did not load within 120s")
}

// parseDistribution decodes strings like "2A+1C5+C8" into rooms of
// (adults, children, childAges). Returns one room per "+" group.
type distributionRoom struct {
	Adults    int
	Children  int
	ChildAges []int
}

func parseDistribution(s string) []distributionRoom {
	var rooms []distributionRoom
	for _, group := range strings.Split(s, "+") {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		room := distributionRoom{}
		// Split adults (digits) from children/ages.
		i := 0
		for i < len(group) && group[i] >= '0' && group[i] <= '9' {
			i++
		}
		if i > 0 {
			n, err := strconv.Atoi(group[:i])
			if err == nil {
				room.Adults = n
			}
		}
		rest := group[i:]
		// Rest starts with 'A' or 'C'.
		if strings.HasPrefix(rest, "A") {
			// no children in this room group
		} else if strings.HasPrefix(rest, "C") {
			agesPart := rest[1:]
			// Either bare digit or range "0-2"
			if idx := strings.Index(agesPart, "-"); idx >= 0 {
				age, err := strconv.Atoi(agesPart[:idx])
				if err == nil {
					room.Children = 1
					room.ChildAges = []int{age}
				}
			} else if len(agesPart) > 0 {
				// Could be multiple children like "C5C8" but the
				// original format uses "C5+C8" split by +. Inside a
				// single group we expect one child max.
				age, err := strconv.Atoi(agesPart)
				if err == nil {
					room.Children = 1
					room.ChildAges = []int{age}
				}
			}
		}
		rooms = append(rooms, room)
	}
	return rooms
}

func hasDistribution(s string) bool {
	return len(parseDistribution(s)) > 0 && strings.Contains(strings.ToUpper(s), "A")
}

// buildDistributionURL converts parsed room distribution into the URL
// format expected by Travel Compositor: "adults~~children~~childAges"
// joined by "::" for multiple rooms. Example: 2A -> "2~~0~~",
// 1A+1C5 -> "1~~0~~::1~~1~~5".
func buildDistributionURL(rooms []distributionRoom) string {
	if len(rooms) == 0 {
		return "2~~0~~"
	}
	var parts []string
	for _, r := range rooms {
		if len(r.ChildAges) == 0 {
			parts = append(parts, fmt.Sprintf("%d~~%d~~", r.Adults, r.Children))
		} else {
			ages := strings.Trim(strings.Join(strings.Fields(fmt.Sprint(r.ChildAges)), " "), " ")
			parts = append(parts, fmt.Sprintf("%d~~%d~~%s", r.Adults, r.Children, ages))
		}
	}
	return strings.Join(parts, "::")
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func mustParseURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		log.Fatalf("mustParseURL(%q): %v", s, err)
	}
	return u
}

// httpLoginClient performs a full-page POST to /home and returns a
// pre-authenticated http.Client. The client preserves the session
// cookies across subsequent requests and uses HTTP/1.1 to avoid the
// server silently rejecting HTTP/2 from automation clients.
//
// Returns an error if the post-login body does not contain the
// "Cerrar sesión" indicator (i.e. login was rejected).
func (p *Provider) httpLoginClient(ctx context.Context) (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{
		ForceAttemptHTTP2: false,
		DialContext:       (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
	}
	client := &http.Client{
		Jar:       jar,
		Transport: tr,
		Timeout:   60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return nil
		},
	}

	// GDPR consent cookie: the browser sets this after the user accepts
	// the consent banner. Without it the server silently rejects login.
	consentURL, _ := url.Parse("https://www.tiptravelya.com/")
	jar.SetCookies(consentURL, []*http.Cookie{
		{Name: "COOKIES_CONSENT", Value: "CONSENT_ADS|CONSENT_ANALYSIS|CONSENT_TECHNICAL|CONSENT_PERFORMANCE"},
		{Name: "travel-agent-tooltip", Value: ""},
	})

	// 1) GET /home to obtain ViewState.
	getReq, _ := http.NewRequestWithContext(ctx, "GET", "https://www.tiptravelya.com/home", nil)
	getReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:152.0) Gecko/20100101 Firefox/152.0")
	getReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	getReq.Header.Set("Accept-Language", "es-AR,es;q=0.9,en-US;q=0.8,en;q=0.7")
	r1, err := client.Do(getReq)
	if err != nil {
		return nil, fmt.Errorf("GET /home: %w", err)
	}
	b1, _ := io.ReadAll(r1.Body)
	r1.Body.Close()

	vsRegex := regexp.MustCompile(`name="javax\.faces\.ViewState"[^>]*value="([^"]+)"`)
	vsMatch := vsRegex.FindStringSubmatch(string(b1))
	if len(vsMatch) < 2 {
		vsRegex2 := regexp.MustCompile(`value="([^"]+)"[^>]*name="javax\.faces\.ViewState"`)
		vsMatch = vsRegex2.FindStringSubmatch(string(b1))
	}
	if len(vsMatch) < 2 {
		return nil, errors.New("could not extract ViewState from /home")
	}

	// 2) POST /home (full form, no Faces-Request partial/ajax).
	form := url.Values{}
	form.Set("micrositeId", "tiptravel")
	form.Set("micrositeId", "tiptravel") // real form has it twice
	form.Set("j_id_4p_2_1_1:login-content:login:Email", p.cfg.Username)
	form.Set("j_id_4p_2_1_1:login-content:login:j_password", p.cfg.Password)
	form.Set("j_id_4p_2_1_1:login-content:login:remember", "true")
	form.Set("j_id_4p_2_1_1:login-content:login:signin", "Siguiente")
	form.Set("j_id_4p_2_1_1:login-content:login:requestURI", "/home")
	form.Set("javax.faces.ViewState", vsMatch[1])
	form.Set("headerForm_SUBMIT", "1")

	postReq, _ := http.NewRequestWithContext(ctx, "POST", "https://www.tiptravelya.com/home", strings.NewReader(form.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.Header.Set("Referer", "https://www.tiptravelya.com/home")
	postReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:152.0) Gecko/20100101 Firefox/152.0")
	postReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	postReq.Header.Set("Accept-Language", "es-AR,es;q=0.9,en-US;q=0.8,en;q=0.7")

	r2, err := client.Do(postReq)
	if err != nil {
		return nil, fmt.Errorf("POST /home: %w", err)
	}
	b2, _ := io.ReadAll(r2.Body)
	r2.Body.Close()

	body := string(b2)
	if !strings.Contains(body, "Cerrar sesi") {
		return nil, fmt.Errorf("post-login body missing 'Cerrar sesión' indicator (len=%d)", len(body))
	}

	return client, nil
}

// HTTPClientForTest exposes the authenticated http.Client to tests in
// the cmd/ tree (which cannot call unexported helpers). Production code
// should use Run().
func (p *Provider) HTTPClientForTest(ctx context.Context) (*http.Client, error) {
	return p.httpLoginClient(ctx)
}

// HTTPFetchForTest is the test-exported variant of httpFetch.
func (p *Provider) HTTPFetchForTest(client *http.Client, urlStr string) (string, error) {
	return p.httpFetch(client, urlStr)
}

// ExtractPricesFromHTMLForTest exposes extractPricesFromHTML for
// integration tests in cmd/.
func (p *Provider) ExtractPricesFromHTMLForTest(html string, order *api.ScrapingOrder) map[string]map[string]interface{} {
	return p.extractPricesFromHTML(html, order)
}

// httpFetch performs a GET with the pre-authenticated client and
// returns the response body as a string.
func (p *Provider) httpFetch(client *http.Client, urlStr string) (string, error) {
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:152.0) Gecko/20100101 Firefox/152.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "es-AR,es;q=0.9,en-US;q=0.8,en;q=0.7")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// extractPricesFromHTML scans the search page HTML for price blocks.
// Unlike the chromedp JS approach this works without a JS runtime,
// which is what we need since the server silently blocks AJAX login
// from automation clients.
//
// Returns a map keyed by stable result id. Each value contains the
// price, currency, the order context, and (when available) the
// surrounding hotel/room/board text for traceability.
func (p *Provider) extractPricesFromHTML(html string, order *api.ScrapingOrder) map[string]map[string]interface{} {
	out := make(map[string]map[string]interface{})
	seen := map[float64]bool{}
	cardsFound := 0

	// Strategy 1: extract from structured price cards.
	// The Travel Compositor renders prices inside
	//   <b class="c-card__idea-price__primary ...">US$1,068</b>
	// We capture ALL prices — the user wants to see la variación de precios.
	cardRe := regexp.MustCompile(`(?is)<b[^>]*class="[^"]*c-card__idea-price__primary[^"]*"[^>]*>\s*([^<]+?)\s*</b>`)
	priceRe := regexp.MustCompile(`(?i)(?:USD|ARS|EUR|US\$|\$|€|R\$)\s*([0-9]{1,3}(?:[.,][0-9]{3})*(?:[.,][0-9]{2})?)`)
	for i, m := range cardRe.FindAllStringSubmatch(html, -1) {
		if len(m) < 2 {
			continue
		}
		priceMatch := priceRe.FindStringSubmatch(m[1])
		if len(priceMatch) < 2 {
			continue
		}
		price, ok := parsePrice(priceMatch[1])
		if !ok || price < 100 {
			continue
		}
		if seen[price] {
			continue
		}
		seen[price] = true

		ctxStart := strings.Index(html, m[1])
		ctxWindow := html
		if ctxStart > 0 {
			lo := ctxStart - 800
			if lo < 0 {
				lo = 0
			}
			hi := ctxStart + 800
			if hi > len(html) {
				hi = len(html)
			}
			ctxWindow = html[lo:hi]
		}

		out[fmt.Sprintf("card_%d", i)] = map[string]interface{}{
			"hotel":    order.HotelName,
			"hotel_id": order.HotelID,
			"price":    price,
			"currency": "USD",
			"raw":      strings.TrimSpace(m[1]),
			"context":  truncate(ctxWindow, 500),
			"matched":  strings.Contains(ctxWindow, order.HotelName) || strings.Contains(ctxWindow, order.HotelID),
		}
		cardsFound++
	}

	// Strategy 2: URL-directa style (single accommodation page).
	if strings.Contains(html, order.HotelID) {
		idx := strings.Index(html, order.HotelID)
		window := html
		if idx > 0 {
			start := idx - 500
			if start < 0 {
				start = 0
			}
			end := idx + 1500
			if end > len(html) {
				end = len(html)
			}
			window = html[start:end]
		}
		m := priceRegex.FindStringSubmatch(window)
		if len(m) >= 2 {
			price, _ := parsePrice(m[1])
			if price >= 100 {
				out["direct"] = map[string]interface{}{
					"hotel":    order.HotelName,
					"hotel_id": order.HotelID,
					"room":     order.RoomType,
					"meal":     order.MealPlan,
					"price":    price,
					"currency": "USD",
				}
			}
		}
	}

	out["search_page"] = map[string]interface{}{
		"hotel":        order.HotelName,
		"hotel_id":     order.HotelID,
		"html_len":     float64(len(html)),
		"prices_found": float64(cardsFound),
		"booked_price": order.BookedPrice,
	}

	return out
}

func (p *Provider) extractPricesFromBrowser(ctx context.Context, order *api.ScrapingOrder) (map[string]map[string]interface{}, error) {
	var html string
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`document.documentElement.outerHTML`, &html),
	); err != nil {
		return nil, fmt.Errorf("fetching page HTML via CDP: %w", err)
	}
	return p.extractPricesFromHTML(html, order), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}



// priceRegex matches currency-formatted numbers like "US$1,068",
// "USD 1.234,56", "ARS 12.345,00". The decimal portion is optional
// because Travel Compositor renders whole-dollar amounts without
// trailing decimals (e.g. "US$1,068" rather than "US$1,068.00").
var priceRegex = regexp.MustCompile(`(?i)(?:USD|ARS|EUR|US\$|\$|€|R\$)\s*([0-9]{1,3}(?:[.,][0-9]{3})*(?:[.,][0-9]{2})?)`)

// altPriceRegex matches bare numbers with thousands separators
// (decimal optional, same rationale as priceRegex).
var altPriceRegex = regexp.MustCompile(`([0-9]{1,3}(?:[.,][0-9]{3})+(?:[.,][0-9]{2})?)`)

// extractPrices pulls the visible price list from the search results page.
// It tries to find the row matching the order's hotel/room and returns a
// map keyed by result id.

// parsePrice normalizes currency-formatted strings to a float64.
// Handles "1,068" (US thousands), "1.234,56" (European thousands+decimal),
// "1,234.56" (US thousands+decimal), and bare integers. Returns
// (value, true) on success.
//
// Disambiguation rule: the last separator decides. If the group
// after the last separator is exactly 3 digits, it is a thousands
// separator; otherwise it is a decimal separator.
func parsePrice(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	lastDot := strings.LastIndex(s, ".")
	lastComma := strings.LastIndex(s, ",")

	switch {
	case lastDot == -1 && lastComma == -1:
		// No separators: plain integer.
		v, err := parseFloat(s)
		return v, err == nil

	case lastDot != -1 && lastComma != -1:
		// Both present: whichever is last wins.
		if lastComma > lastDot {
			// European: 1.234,56
			s = strings.ReplaceAll(s, ".", "")
			s = strings.Replace(s, ",", ".", 1)
		} else {
			// US: 1,234.56
			s = strings.ReplaceAll(s, ",", "")
		}
		v, err := parseFloat(s)
		return v, err == nil

	case lastComma != -1:
		// Only comma. If the chunk after the comma has 3 digits, treat
		// it as US thousands (1,068 -> 1068). Otherwise treat it as
		// European decimal (1,06 -> 1.06).
		after := s[lastComma+1:]
		if len(after) == 3 && allDigits(after) {
			s = strings.ReplaceAll(s, ",", "")
		} else {
			s = strings.Replace(s, ",", ".", 1)
		}
		v, err := parseFloat(s)
		return v, err == nil

	default:
		// Only dot. If the chunk after the dot has 3 digits and there
		// is no other dot, treat as European thousands (1.234 -> 1234).
		// Otherwise treat as US decimal (1.06 -> 1.06).
		after := s[lastDot+1:]
		if len(after) == 3 && strings.Count(s, ".") == 1 && allDigits(after) {
			s = strings.ReplaceAll(s, ".", "")
		}
		v, err := parseFloat(s)
		return v, err == nil
	}
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func parseFloat(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(s, "%f", &f)
	return f, err
}