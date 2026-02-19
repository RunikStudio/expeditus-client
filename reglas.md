# Web Interaction Guide - [NOMBRE DE LA WEB]

> **Target URL:** `https://ejemplo.com/login`  
> **Purpose:** [Login + Extracción de datos / Solo login / Navegación automatizada]  
> **Last Updated:** 2026-02-18  
> **Authentication Method:** [Cookies / JWT / OAuth / Basic Auth]

---

## 1. Pre-requisites

- [ ] Account credentials (username/password or API key)
- [ ] 2FA enabled? [Yes/No] — If yes, document bypass method
- [ ] Rate limiting awareness: [X requests per minute]

---

## 2. Authentication Flow

### 2.1 Login Page
```
URL: https://ejemplo.com/login
Method: POST / GET
Form selectors (if applicable):
  - Username field: `input[name="username"]`
  - Password field: `input[name="password"]`
  - Submit button: `button[type="submit"]`
```

### 2.2 Auth Request Details
```http
POST /api/auth/login HTTP/1.1
Host: ejemplo.com
Content-Type: application/json

{
  "username": "{{USERNAME}}",
  "password": "{{PASSWORD}}"
}
```

### 2.3 Response Handling
| Scenario | Expected Response | Action |
|----------|-------------------|--------|
| Success | `200 OK` + `Set-Cookie: session=...` | Store session cookie |
| Invalid credentials | `401 Unauthorized` | Log error, do not retry |
| Rate limited | `429 Too Many Requests` | Wait X seconds, retry |

### 2.4 Session Persistence
- **Cookie name:** `session_id`
- **Expires:** [e.g., 24 hours]
- **Storage:** Save to `cookies.json` or memory

---

## 3. Post-Login Navigation

### 3.1 Landing Page After Login
```
URL: https://ejemplo.com/dashboard
Expected element: `div.dashboard-container`
Verification: Check for `text: "Welcome back"`
```

### 3.2 Key Pages / Actions
| Action | URL | Method | Payload | Expected Response |
|--------|-----|--------|---------|-------------------|
| Get profile | `/api/user/profile` | GET | — | JSON with user data |
| Update settings | `/api/user/settings` | POST | `{theme: "dark"}` | `200 OK` |

---

## 4. Error Handling

| Error Code | Meaning | Recommended Action |
|------------|---------|-------------------|
| `401` | Session expired | Re-login, refresh token |
| `403` | Forbidden | Check permissions, stop |
| `500` | Server error | Retry after 5s, max 3 times |

---

## 5. Anti-Detection Measures (if applicable)

- [ ] Use realistic User-Agent
- [ ] Add delays between requests (1-3s)
- [ ] Handle CAPTCHA via [service/manual]
- [ ] Rotate IP if rate limited

---

## 6. Example Code Snippet (Playwright/Puppeteer)

```typescript
// Example login flow using Playwright
import { chromium } from 'playwright';

async function login(username: string, password: string) {
  const browser = await chromium.launch();
  const context = await browser.newContext();
  const page = await context.newPage();

  await page.goto('https://ejemplo.com/login');
  
  await page.fill('input[name="username"]', username);
  await page.fill('input[name="password"]', password);
  await page.click('button[type="submit"]');
  
  // Wait for navigation
  await page.waitForURL('**/dashboard');
  
  // Save session
  const cookies = await context.cookies();
  await fs.writeFile('cookies.json', JSON.stringify(cookies));
  
  return { browser, context, page };
}
```

---

## 7. Notes & Gotchas

- ⚠️ **Important:** The login form has a hidden CSRF token: `input[name="_csrf"]`
- ⚠️ **Timing:** Wait for `networkidle` after login before navigating
- ⚠️ **Session:** Token expires exactly at midnight UTC

---

## 8. Verification Checklist

- [ ] Login succeeds with valid credentials
- [ ] Session persists across requests
- [ ] Logout properly clears session
- [ ] Error responses are handled gracefully
