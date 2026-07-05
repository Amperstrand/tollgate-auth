package ocpi

import (
	"html/template"
	"net/http"
	"strconv"
	"time"
)

// handleDashboard renders the single-page HTML dashboard at GET /.
// It includes HTMX for live polling of the peer-status panel.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	snap := s.store.Snapshot()

	s.charger.mu.Lock()
	chargerState := s.charger.State
	var liveKwh float64
	var sessionInfo *LiveSession
	if s.charger.Session != nil {
		sessionInfo = s.charger.Session
		liveKwh = sessionInfo.PowerKw * time.Since(sessionInfo.StartedAt).Hours()
	}
	s.charger.mu.Unlock()

	tmplData := struct {
		Snapshot
		ChargerState string
		LiveKwh      float64
		Session      *LiveSession
	}{
		Snapshot:     snap,
		ChargerState: chargerState,
		LiveKwh:      liveKwh,
		Session:      sessionInfo,
	}

	tmpl := template.Must(template.New("dash").Funcs(template.FuncMap{
		"fmtTime": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			return t.UTC().Format("2006-01-02 15:04:05Z")
		},
		"fmtDur": func(sec int) string {
			if sec <= 0 {
				return "0s"
			}
			d := time.Duration(sec) * time.Second
			if d.Hours() >= 1 {
				return d.Round(time.Minute).String()
			}
			return d.Round(time.Second).String()
		},
		"fmtKwh": func(k float64) string {
			return strconv.FormatFloat(k, 'f', 3, 64)
		},
		"fmtCost": func(k float64) string {
			return strconv.FormatFloat(k, 'f', 2, 64)
		},
		"trunc": func(s string, n int) string {
			if len(s) <= n {
				return s
			}
			return s[:n] + "…"
		},
	}).Parse(dashboardHTML))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, tmplData); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

const dashboardHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>tollgate-auth · OCPI eMSP</title>
<script src="https://unpkg.com/htmx.org@1.9.12"></script>
<style>
  :root {
    --bg: #0d1117; --panel: #161b22; --border: #30363d;
    --text: #c9d1d9; --muted: #8b949e; --accent: #58a6ff;
    --ok: #3fb950; --warn: #d29922; --err: #f85149;
    --mono: ui-monospace, SFMono-Regular, Menlo, monospace;
  }
  * { box-sizing: border-box; }
  body {
    background: var(--bg); color: var(--text);
    font: 14px/1.5 system-ui, sans-serif;
    margin: 0; padding: 2rem 1rem;
  }
  h1 { font-size: 1.5rem; margin: 0 0 0.25rem; }
  h2 { font-size: 1.05rem; margin: 0 0 0.75rem; color: var(--accent); font-weight: 600; }
  .grid { display: grid; gap: 1rem; grid-template-columns: 1fr; max-width: 1100px; margin: 0 auto; }
  @media (min-width: 900px) { .grid { grid-template-columns: 1fr 1fr; } }
  .panel { background: var(--panel); border: 1px solid var(--border); border-radius: 8px; padding: 1rem 1.25rem; }
  .panel.full { grid-column: 1 / -1; }
  .muted { color: var(--muted); font-size: 0.85rem; }
  .badge {
    display: inline-block; padding: 2px 8px; border-radius: 12px;
    font-size: 0.75rem; font-weight: 600; text-transform: uppercase;
    background: var(--border); color: var(--text);
  }
  .badge.ok { background: rgba(63,185,80,0.18); color: var(--ok); }
  .badge.err { background: rgba(248,81,73,0.18); color: var(--err); }
  .badge.warn { background: rgba(210,153,34,0.18); color: var(--warn); }
  table { width: 100%; border-collapse: collapse; font-size: 0.85rem; }
  th, td { padding: 6px 8px; text-align: left; border-bottom: 1px solid var(--border); }
  th { color: var(--muted); font-weight: 500; font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.04em; }
  td.mono, th.mono { font-family: var(--mono); font-size: 0.8rem; }
  input, button, textarea {
    background: var(--bg); color: var(--text);
    border: 1px solid var(--border); border-radius: 6px;
    padding: 6px 10px; font: inherit;
  }
  button { cursor: pointer; background: var(--accent); color: #0d1117; border-color: var(--accent); font-weight: 600; }
  button:hover { opacity: 0.9; }
  textarea { width: 100%; min-height: 60px; font-family: var(--mono); font-size: 0.8rem; }
  .row { display: flex; gap: 0.5rem; align-items: center; flex-wrap: wrap; }
  .kv { display: grid; grid-template-columns: max-content 1fr; gap: 4px 12px; font-size: 0.85rem; }
  .kv .k { color: var(--muted); }
  .result {
    margin-top: 0.75rem; padding: 0.5rem 0.75rem; border-radius: 6px;
    font-family: var(--mono); font-size: 0.85rem;
    background: rgba(88,166,255,0.08); border: 1px solid var(--accent);
    word-break: break-all;
  }
  .result.err { background: rgba(248,81,73,0.08); border-color: var(--err); color: var(--err); }
  a { color: var(--accent); }
  code { font-family: var(--mono); background: var(--bg); padding: 1px 4px; border-radius: 3px; }

  .charger-box {
    display: flex; align-items: center; gap: 2rem; flex-wrap: wrap;
    padding: 1.5rem; border-radius: 12px; border: 2px solid var(--border);
    transition: all 0.3s; background: var(--bg);
  }
  .charger-box.AVAILABLE { border-color: #30363d; }
  .charger-box.CHARGING {
    border-color: var(--ok);
    box-shadow: 0 0 24px rgba(63,185,80,0.25);
    animation: pulse-green 2s ease-in-out infinite;
  }
  .charger-box.BLOCKED { border-color: var(--err); box-shadow: 0 0 24px rgba(248,81,73,0.2); }
  @keyframes pulse-green {
    0%, 100% { box-shadow: 0 0 24px rgba(63,185,80,0.25); }
    50% { box-shadow: 0 0 40px rgba(63,185,80,0.5); }
  }
  .charger-icon {
    font-size: 4rem; line-height: 1; width: 80px; height: 80px;
    display: flex; align-items: center; justify-content: center;
    border-radius: 50%; background: var(--panel); border: 2px solid var(--border);
  }
  .charger-box.CHARGING .charger-icon { border-color: var(--ok); }
  .charger-box.BLOCKED .charger-icon { border-color: var(--err); }
  .charger-info { flex: 1; min-width: 200px; }
  .charger-state {
    font-size: 1.5rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.05em;
  }
  .AVAILABLE .charger-state { color: var(--muted); }
  .CHARGING .charger-state { color: var(--ok); }
  .BLOCKED .charger-state { color: var(--err); }
  .charger-meter { font-size: 2rem; font-family: var(--mono); margin-top: 0.25rem; }
  .charger-meter .unit { font-size: 1rem; color: var(--muted); }
  .charger-meter .power { font-size: 0.85rem; color: var(--muted); display: block; }
  .charger-form { display: flex; gap: 0.5rem; flex: 1; min-width: 280px; }
  .charger-form input { flex: 1; font-family: var(--mono); font-size: 0.8rem; }
</style>
</head>
<body>
<div class="grid">

  <div class="panel full">
    <h1>tollgate-auth · OCPI 2.2.1 eMSP</h1>
    <div class="muted">EV charging gateway reusing the same Cashu pipeline as SSH + RADIUS. Pay-per-session with ecash.</div>
  </div>

  <div class="panel full">
    <h2>Virtual Charger</h2>
    <div class="charger-box {{.ChargerState}}" id="charger-box">
      <div class="charger-icon">⚡</div>
      <div class="charger-info">
        <div class="charger-state" id="charger-state-text">{{.ChargerState}}</div>
        {{if eq .ChargerState "CHARGING"}}
        <div class="charger-meter">
          <span id="live-kwh">{{printf "%.3f" .LiveKwh}}</span><span class="unit"> kWh</span>
          <span class="power" id="charger-power">{{if .Session}}{{.Session.PowerKw}}{{else}}7.4{{end}} kW · {{if .Session}}{{.Session.CreditAmount}} {{.Session.Unit}} credit{{end}}</span>
        </div>
        {{else}}
        <div class="muted" style="margin-top:0.25rem">Ready. Paste a Cashu token to plug in and charge.</div>
        {{end}}
      </div>
      <div class="charger-controls">
        {{if eq .ChargerState "AVAILABLE"}}
        <div class="charger-form">
          <input type="text" id="charge-cashu" placeholder="cashuB... or cashuA..." autocomplete="off">
          <button type="button" onclick="startCharge()">⚡ Plug In</button>
        </div>
        {{else if eq .ChargerState "CHARGING"}}
        <button type="button" onclick="stopCharge()" style="background:var(--err);border-color:var(--err);color:#fff">⏹ Stop</button>
        {{end}}
      </div>
    </div>
  </div>

  <div class="panel" hx-get="/api/snapshot" hx-trigger="load, every 5s" hx-swap="none">
    <h2>Peer &amp; Status</h2>
    {{if .Peer}}
      <div class="row"><span class="badge ok">connected</span></div>
      <div class="kv" style="margin-top:0.5rem">
        <span class="k">Party</span><span class="mono">{{.Peer.TheirCountry}} · {{.Peer.TheirParty}}</span>
        <span class="k">Handshake</span><span>{{fmtTime .Peer.HandshakedAt}}</span>
        <span class="k">Token C</span><span class="mono">{{trunc .Peer.OurTokenC 8}}</span>
      </div>
    {{else}}
      <div class="row"><span class="badge warn">no peer</span></div>
      <div class="muted" style="margin-top:0.5rem">
        Configure OCPPLab (or your CPO) to handshake against
        <code>/ocpi/versions</code> on this server.
      </div>
    {{end}}
  </div>

  <div class="panel full">
    <h2>OCPI CPO Simulator</h2>
    <div class="muted" style="margin-bottom:0.75rem">Triggers real OCPI 2.2.1 HTTP messages against this server. Shows the protocol exchange a CPO would make.</div>
    <div class="row" style="gap:0.5rem; margin-bottom:0.75rem">
      <button type="button" onclick="simConnect()" style="font-size:0.8rem">1. CPO Handshake</button>
      <button type="button" onclick="simAuthorize()" style="font-size:0.8rem">2. Send Authorize</button>
      <button type="button" onclick="simCDR()" style="font-size:0.8rem">3. Send CDR</button>
      <button type="button" onclick="clearOCPIlog()" style="font-size:0.8rem;opacity:0.6">Clear</button>
    </div>
    <div id="ocpi-log" style="background:var(--bg);border:1px solid var(--border);border-radius:6px;padding:0.5rem;font-family:var(--mono);font-size:0.75rem;max-height:200px;overflow-y:auto;line-height:1.6">
      <span class="muted">OCPI protocol messages will appear here...</span>
    </div>
  </div>

  <div class="panel">
    <h2>Issue OCPI Token from Cashu</h2>
    <form id="prepay-form">
      <textarea id="cashu" placeholder="cashuB... or cashuA..." autocomplete="off"></textarea>
      <div class="row" style="margin-top:0.5rem">
        <button type="submit">Mint OCPI Token</button>
        <span class="muted">Pays via testnut.cashu.space (PoC)</span>
      </div>
    </form>
    <div id="prepay-result"></div>
  </div>

  <div class="panel full">
    <h2>Prepay Tokens</h2>
    {{if .Tokens}}
    <table>
      <thead><tr>
        <th>UID</th><th>Contract</th><th>Sats</th><th>Allotment</th>
        <th>Authorized</th><th>Used</th><th>Mint</th>
      </tr></thead>
      <tbody>
      {{range .Tokens}}
        <tr>
          <td class="mono">{{.UID}}</td>
          <td class="mono muted">{{.ContractID}}</td>
          <td>{{.CreditAmount}}</td>
          <td>{{fmtDur .AllotmentSec}}</td>
          <td>{{if .AuthorizedAt}}<span class="badge ok">yes</span>{{else}}<span class="muted">no</span>{{end}}</td>
          <td>{{if .Used}}<span class="badge warn">used</span>{{else}}<span class="badge ok">live</span>{{end}}</td>
          <td class="mono muted" style="font-size:0.75rem">{{trunc .MintURL 40}}</td>
        </tr>
      {{end}}
      </tbody>
    </table>
    {{else}}
      <div class="muted">No prepay tokens issued yet.</div>
    {{end}}
  </div>

  <div class="panel">
    <h2>Recent Authorize Calls</h2>
    {{if .AuthzLog}}
    <table>
      <thead><tr><th>At</th><th>UID</th><th>Result</th></tr></thead>
      <tbody>
      {{range .AuthzLog}}
        <tr>
          <td class="mono" style="font-size:0.75rem">{{fmtTime .At}}</td>
          <td class="mono">{{trunc .UID 16}}</td>
          <td>
            {{if eq .Allowed "ALLOWED"}}<span class="badge ok">{{.Allowed}}</span>
            {{else if eq .Allowed "DISALLOWED"}}<span class="badge err">{{.Allowed}}</span>
            {{else}}<span class="badge warn">{{.Allowed}}</span>{{end}}
            <span class="muted" style="font-size:0.75rem; margin-left:0.5rem">{{.Reason}}</span>
          </td>
        </tr>
      {{end}}
      </tbody>
    </table>
    {{else}}
      <div class="muted">No authorize calls received.</div>
    {{end}}
  </div>

  <div class="panel">
    <h2>Sessions &amp; CDRs</h2>
    {{if .Sessions}}
    <h3 style="margin:0 0 0.5rem;font-size:0.85rem;color:var(--muted)">Sessions ({{len .Sessions}})</h3>
    <table>
      <tbody>
      {{range .Sessions}}
        <tr>
          <td class="mono">{{.ID}}</td>
          <td><span class="badge">{{.Status}}</span></td>
          <td>{{fmtKwh .Kwh}} kWh</td>
        </tr>
      {{end}}
      </tbody>
    </table>
    {{else}}
      <div class="muted">No active sessions.</div>
    {{end}}

    {{if .CDRs}}
    <h3 style="margin:1rem 0 0.5rem;font-size:0.85rem;color:var(--muted)">CDRs ({{len .CDRs}})</h3>
    <table>
      <tbody>
      {{range .CDRs}}
        <tr>
          <td class="mono">{{.ID}}</td>
          <td>{{fmtKwh .Kwh}} kWh</td>
          <td>{{.Currency}} {{fmtCost .TotalCost}}</td>
        </tr>
      {{end}}
      </tbody>
    </table>
    {{end}}
  </div>

</div>

<script>
function logOCPI(direction, label, detail) {
  const log = document.getElementById('ocpi-log');
  if (!log) return;
  if (log.querySelector('.muted')) log.innerHTML = '';
  const ts = new Date().toLocaleTimeString();
  const dirColor = direction === '\u2192' ? 'var(--accent)' : 'var(--ok)';
  const entry = document.createElement('div');
  entry.style.cssText = 'border-bottom:1px solid var(--border);padding:3px 0';
  entry.innerHTML = '<span style="color:var(--muted)">' + ts + '</span> <span style="color:' + dirColor + ';font-weight:bold">' + direction + '</span> <strong>' + label + '</strong>' + (detail ? '<br><span style="color:var(--muted);padding-left:1.5rem">' + detail.slice(0,200) + '</span>' : '');
  log.insertBefore(entry, log.firstChild);
}

function clearOCPIlog() {
  const log = document.getElementById('ocpi-log');
  if (log) log.innerHTML = '<span class="muted">Cleared.</span>';
}

async function simConnect() {
  logOCPI('→', 'POST /ocpi/emsp/2.2.1/credentials', 'CPO initiating handshake...');
  try {
    const resp = await fetch('/ocpi/emsp/2.2.1/credentials', {
      method: 'POST',
      headers: {'Content-Type': 'application/json', 'Authorization': 'Token sim-bootstrap-001'},
      body: JSON.stringify({token: 'sim-cpo-token-b', url: 'https://simulated.cpo.example/ocpi/cpo/2.2.1/version_details', party_id: 'SIM', country_code: 'NO'})
    });
    const data = await resp.json();
    const tok = data.data && data.data.token ? data.data.token : '?';
    logOCPI('\u2190', 'Credentials accepted \u2014 Token C: ' + tok.slice(0,12) + '\u2026', 'Party: ' + (data.data && data.data.country_code ? data.data.country_code : '?') + '/' + (data.data && data.data.party_id ? data.data.party_id : '?') + ' \u2014 handshake complete');
  } catch (e) {
    logOCPI('←', 'ERROR', e.message);
  }
}

async function simAuthorize() {
  const uid = document.querySelector('.mono') ? document.querySelector('.mono').textContent.trim().slice(0,20) : 'OCPI-TEST';
  logOCPI('\u2192', 'POST /ocpi/emsp/2.2.1/tokens/' + uid + '/authorize', 'CPO requesting authorization for token ' + uid + '...');
  try {
    const resp = await fetch('/ocpi/emsp/2.2.1/tokens/' + uid + '/authorize', {method: 'POST'});
    const data = await resp.json();
    const allowed = (data.data && data.data.allowed) ? data.data.allowed : '?';
    const icon = allowed === 'ALLOWED' ? '\u2705' : '\u274c';
    logOCPI('\u2190', 'Authorize result: ' + icon + ' ' + allowed, (data.data && data.data.authorization_reference) ? 'ref: ' + data.data.authorization_reference.slice(0,16) + '\u2026' : '');
    if (allowed === 'ALLOWED') logOCPI('', 'Charger would START charging now.', 'Energy flow authorized by Cashu payment verification.');
  } catch (e) {
    logOCPI('←', 'ERROR', e.message);
  }
}

async function simCDR() {
  const sessionId = 'sess-sim-' + Date.now().toString(36);
  const kwh = (Math.random() * 15 + 5).toFixed(3);
  const cost = (parseFloat(kwh) * 2.50).toFixed(2);
  logOCPI('\u2192', 'POST /ocpi/emsp/2.2.1/cdrs', 'CPO sending CDR: ' + kwh + ' kWh, NOK ' + cost);
  try {
    const resp = await fetch('/ocpi/emsp/2.2.1/cdrs', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({id: 'cdr-sim-' + Date.now().toString(36), auth_id: 'OCPI-TEST', kwh: parseFloat(kwh), total_cost: parseFloat(cost), currency: 'NOK', location_id: 'loc-sim-001', start_date: new Date(Date.now()-3600000).toISOString(), stop_date: new Date().toISOString(), last_updated: new Date().toISOString()})
    });
    const data = await resp.json();
    logOCPI('\u2190', 'CDR accepted: status ' + data.status_code, kwh + ' kWh delivered, NOK ' + cost + ' billed.');
    setTimeout(() => location.reload(), 1000);
  } catch (e) {
    logOCPI('←', 'ERROR', e.message);
  }
}

document.getElementById('prepay-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const token = document.getElementById('cashu').value.trim();
  const out = document.getElementById('prepay-result');
  if (!token) { out.innerHTML = '<div class="result err">Paste a Cashu token first.</div>'; return; }
  out.innerHTML = '<div class="result">Verifying with mint…</div>';
  try {
    const resp = await fetch('/api/prepay', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({cashu_token: token})
    });
    const data = await resp.json();
    if (data.status_code !== 1000 || !data.data) {
      out.innerHTML = '<div class="result err">' + (data.status_message || 'verify failed') + '</div>';
      return;
    }
    const r = data.data;
    out.innerHTML =
      '<div class="result">' +
        '<strong>OCPI Token UID:</strong> <code>' + r.uid + '</code><br>' +
        '<strong>Allotment:</strong> ' + Math.floor(r.allotment_sec/60) + ' min (' + r.allotment_sec + 's)<br>' +
        '<strong>Credit:</strong> ' + r.credit_amount + ' ' + (r.unit || 'sat') + ' from ' + r.mint_url + '<br>' +
        '<strong>Contract:</strong> <code>' + r.contract_id + '</code>' +
      '</div>';
    document.getElementById('cashu').value = '';
  } catch (err) {
    out.innerHTML = '<div class="result err">fetch error: ' + err.message + '</div>';
  }
});

let chargePollMs = null;

async function startCharge() {
  const token = document.getElementById('charge-cashu').value.trim();
  const box = document.getElementById('charger-box');
  if (!token) { alert('Paste a Cashu token first'); return; }
  const btn = document.querySelector('button:has-text("Plug In")');
  if (btn) { btn.disabled = true; btn.textContent = 'Verifying…'; }
  try {
    const resp = await fetch('/api/charger/start', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({cashu_token: token})
    });
    const data = await resp.json();
    if (data.status_code === 1000 && data.data && data.data.state === 'CHARGING') {
      location.reload();
    } else {
      if (btn) { btn.disabled = false; btn.textContent = '⚡ Plug In'; }
      const msg = data.status_message || 'verify failed';
      const input = document.getElementById('charge-cashu');
      if (input) { input.value = ''; input.placeholder = '❌ ' + msg.slice(0, 80); input.focus(); }
    }
  } catch (err) {
    if (btn) { btn.disabled = false; btn.textContent = '⚡ Plug In'; }
    alert('Error: ' + err.message);
  }
}

async function stopCharge() {
  try {
    const resp = await fetch('/api/charger/stop', {method: 'POST'});
    const data = await resp.json();
    location.reload();
  } catch (err) {
    alert('Error stopping: ' + err.message);
  }
}

function resetCharger() { location.reload(); }

async function pollCharger() {  try {
    const resp = await fetch('/api/charger/status');
    const data = await resp.json();
    if (data.status_code !== 1000 || !data.data) return;
    const st = data.data.state;

    if (st === 'CHARGING' && data.data.kwh !== undefined) {
      const kwhEl = document.getElementById('live-kwh');
      if (kwhEl) kwhEl.textContent = data.data.kwh.toFixed(3);
    }

    const box = document.getElementById('charger-box');
    const stateText = document.getElementById('charger-state-text');
    if (box && stateText) {
      const currentState = box.classList.contains('CHARGING') ? 'CHARGING' :
                           box.classList.contains('BLOCKED') ? 'BLOCKED' : 'AVAILABLE';
      if (st !== currentState) {
        location.reload();
      }
    }
  } catch (e) {}
}

if (document.getElementsByClassName('charger-box')[0]?.classList.contains('CHARGING')) {
  chargePollMs = setInterval(pollCharger, 2000);
}
</script>
</body>
</html>`

// handleAbout renders the static landing page at GET /about.
// Explains the Cashu-gated EV charging model for investors, partners, and CPOs.
// No dynamic data; uses html/template for parity with handleDashboard.
func (s *Server) handleAbout(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/about" {
		http.NotFound(w, r)
		return
	}
	tmpl := template.Must(template.New("about").Parse(aboutHTML))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, nil); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

const aboutHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>tollgate-auth · Cashu-Gated EV Charging</title>
<style>
  :root {
    --bg: #0d1117; --panel: #161b22; --border: #30363d;
    --text: #c9d1d9; --muted: #8b949e; --accent: #58a6ff;
    --ok: #3fb950; --warn: #d29922; --err: #f85149;
    --mono: ui-monospace, SFMono-Regular, Menlo, monospace;
    --btc: #f7931a; --eur: #58a6ff;
  }
  * { box-sizing: border-box; }
  body {
    background: var(--bg); color: var(--text);
    font: 14px/1.5 system-ui, sans-serif;
    margin: 0; padding: 2rem 1rem;
  }
  h1 { font-size: 1.5rem; margin: 0 0 0.25rem; }
  h2 { font-size: 1.05rem; margin: 0 0 0.75rem; color: var(--accent); font-weight: 600; }
  .grid { display: grid; gap: 1rem; grid-template-columns: 1fr; max-width: 1100px; margin: 0 auto; }
  @media (min-width: 900px) { .grid { grid-template-columns: 1fr 1fr; } }
  .panel { background: var(--panel); border: 1px solid var(--border); border-radius: 8px; padding: 1rem 1.25rem; }
  .panel.full { grid-column: 1 / -1; }
  .muted { color: var(--muted); font-size: 0.85rem; }
  a { color: var(--accent); }
  code { font-family: var(--mono); background: var(--bg); padding: 1px 4px; border-radius: 3px; }

  .hero { text-align: center; padding: 2.5rem 1.5rem; }
  .hero h1 { font-size: 2.25rem; margin-bottom: 0.75rem; letter-spacing: -0.02em; }
  .hero h1 .spark { color: var(--ok); }
  .hero .pitch { font-size: 1.1rem; color: var(--text); max-width: 640px; margin: 0 auto 1.5rem; }
  .hero .badges { display: flex; gap: 0.5rem; justify-content: center; flex-wrap: wrap; margin-bottom: 1.5rem; }
  .pill {
    display: inline-block; padding: 4px 10px; border-radius: 999px;
    font-size: 0.75rem; font-weight: 600; text-transform: uppercase;
    letter-spacing: 0.04em; border: 1px solid var(--border); color: var(--muted);
  }
  .pill.accent { color: var(--accent); border-color: var(--accent); background: rgba(88,166,255,0.08); }
  .pill.ok { color: var(--ok); border-color: var(--ok); background: rgba(63,185,80,0.08); }

  .cta {
    display: inline-block; padding: 0.75rem 1.75rem; border-radius: 8px;
    background: var(--accent); color: #0d1117; text-decoration: none;
    font-weight: 700; font-size: 1rem; border: 1px solid var(--accent);
    transition: transform 0.2s, box-shadow 0.2s, opacity 0.2s;
  }
  .cta:hover { opacity: 0.92; transform: translateY(-1px); box-shadow: 0 8px 24px rgba(88,166,255,0.3); }

  .steps { display: grid; gap: 1rem; grid-template-columns: 1fr; }
  @media (min-width: 700px) { .steps { grid-template-columns: repeat(4, 1fr); } }
  .step {
    background: var(--bg); border: 1px solid var(--border); border-radius: 8px;
    padding: 1rem; position: relative;
  }
  .step .num {
    position: absolute; top: -0.7rem; left: 0.75rem;
    background: var(--accent); color: #0d1117;
    width: 1.5rem; height: 1.5rem; border-radius: 50%;
    display: flex; align-items: center; justify-content: center;
    font-weight: 700; font-size: 0.85rem;
  }
  .step .icon { font-size: 1.75rem; margin: 0.25rem 0; }
  .step h3 { margin: 0.25rem 0; font-size: 0.95rem; color: var(--accent); }
  .step p { margin: 0; font-size: 0.82rem; color: var(--muted); }
  @media (min-width: 700px) {
    .step:not(:last-child)::after {
      content: "→"; position: absolute; right: -0.7rem; top: 50%; transform: translateY(-50%);
      color: var(--border); font-size: 1.25rem; font-weight: 700; z-index: 1;
    }
  }

  pre.diagram {
    background: var(--bg); border: 1px solid var(--border); border-radius: 8px;
    padding: 1rem 1.25rem; margin: 0; overflow-x: auto;
    font-family: var(--mono); font-size: 0.78rem; line-height: 1.4;
    color: var(--text);
  }

  .compare-head { display: flex; justify-content: space-between; align-items: baseline; gap: 0.5rem; flex-wrap: wrap; }
  .compare-head .tag {
    font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.05em;
    padding: 2px 8px; border-radius: 4px; font-weight: 700;
  }
  .tag.btc { background: rgba(247,147,26,0.15); color: var(--btc); border: 1px solid rgba(247,147,26,0.4); }
  .tag.eur { background: rgba(88,166,255,0.15); color: var(--eur); border: 1px solid rgba(88,166,255,0.4); }
  .panel.btc-panel { border-top: 3px solid var(--btc); }
  .panel.eur-panel { border-top: 3px solid var(--eur); }
  .compare-list { list-style: none; padding: 0; margin: 0.5rem 0 0; font-size: 0.88rem; }
  .compare-list li { padding: 4px 0 4px 1.4rem; position: relative; color: var(--text); }
  .compare-list li::before { content: "▸"; position: absolute; left: 0; color: var(--muted); }
  .compare-list li.yes::before { content: "✓"; color: var(--ok); }
  .compare-list li.no::before { content: "✕"; color: var(--err); }

  .cpo-code {
    display: block; background: var(--bg); border: 1px solid var(--border); border-radius: 6px;
    padding: 0.75rem 1rem; font-family: var(--mono); font-size: 0.9rem;
    color: var(--accent); margin: 0.5rem 0; word-break: break-all;
  }

  .footer { text-align: center; padding: 1.5rem; color: var(--muted); font-size: 0.8rem; }
  .footer a { text-decoration: none; }
  .footer .cta { margin-bottom: 1rem; }
</style>
</head>
<body>
<div class="grid">

  <!-- HERO -->
  <div class="panel full hero">
    <div class="badges">
      <span class="pill accent">OCPI 2.2.1</span>
      <span class="pill ok">Cashu ecash</span>
      <span class="pill">Party NO/TGA</span>
      <span class="pill">Live PoC</span>
    </div>
    <h1>Cashu-Gated <span class="spark">EV Charging</span></h1>
    <p class="pitch">
      Pay-per-session electric vehicle charging powered by Chaumian ecash.
      Drivers pay in sats, receive a Cashu token, plug in, and the charger starts —
      no accounts, no apps, no PCI scope.
    </p>
    <a class="cta" href="/">⚡ Try the Live Demo</a>
    <div class="muted" style="margin-top:0.75rem">No signup. Test tokens are free on the testnut mint.</div>
  </div>

  <!-- HOW IT WORKS -->
  <div class="panel full">
    <h2>How it works</h2>
    <div class="steps">
      <div class="step">
        <span class="num">1</span>
        <div class="icon">🏦</div>
        <h3>Buy credit</h3>
        <p>Driver sends sats to a Cashu mint and receives blind-signed ecash.</p>
      </div>
      <div class="step">
        <span class="num">2</span>
        <div class="icon">🪙</div>
        <h3>Get Cashu token</h3>
        <p>Mint returns a spendable, transferable, private ecash token.</p>
      </div>
      <div class="step">
        <span class="num">3</span>
        <div class="icon">🔌</div>
        <h3>Plug in</h3>
        <p>Driver pastes the token on the dashboard and hits "Plug In".</p>
      </div>
      <div class="step">
        <span class="num">4</span>
        <div class="icon">⚡</div>
        <h3>Charger starts</h3>
        <p>eMSP verifies ecash, mints an OCPI token, authorizes the session.</p>
      </div>
    </div>
  </div>

  <!-- ARCHITECTURE -->
  <div class="panel full">
    <h2>Architecture</h2>
<pre class="diagram">  DRIVER              MINT              eMSP               CPO
  ──────              ────              ────               ───
     │   ecash swap     │                 │                  │
     │─────────────────▶│                 │                  │
     │◀─────────────────│                 │                  │
     │                                    │                  │
     │   1. paste Cashu token, "Plug In"  │                  │
     │───────────────────────────────────▶│                  │
     │                                    │  2. verify ecash │
     │                                    │─────────────────▶│ (mint)
     │                                    │  3. Authorize    │
     │                                    │─────────────────▶│
     │                                    │  START_SESSION   │
     │                                    │◀─────────────────│
     │   4. kilowatts flow                │                  │
     │◀───────────────────────────────────│                  │
</pre>
    <div class="muted" style="margin-top:0.75rem">
      The <code>tollgate-auth</code> eMSP is the only trust boundary. It speaks OCPI 2.2.1
      northbound to the CPO and Cashu southbound to whichever mint the driver chose.
      The CPO never sees Bitcoin; the mint never sees kilowatts.
    </div>
  </div>

  <!-- COMPARISON HEADER -->
  <div class="panel full">
    <h2 style="margin:0 0 0.25rem">Two settlement models, one gateway</h2>
    <div class="muted">The same OCPI stack settles in either native Bitcoin ecash or provider-issued EUR vouchers.</div>
  </div>

  <!-- MODEL: BTC CASHU -->
  <div class="panel btc-panel">
    <div class="compare-head">
      <h2 style="margin:0;color:var(--btc)">₿ BTC Cashu</h2>
      <span class="tag btc">decentralized</span>
    </div>
    <div class="muted" style="margin-bottom:0.5rem">Driver-side, trustless, mint-agnostic</div>
    <ul class="compare-list">
      <li class="yes">Any Cashu mint (testnut, Mutiny, self-hosted)</li>
      <li class="yes">Settles in sats; spot-converted to kWh at plug time</li>
      <li class="yes">No KYC, no account, no app — bearer ecash</li>
      <li class="yes">Works across every operator running this gateway</li>
      <li class="no">Price volatility absorbed by driver / pricing feed</li>
    </ul>
  </div>

  <!-- MODEL: EUR GIFT CARD -->
  <div class="panel eur-panel">
    <div class="compare-head">
      <h2 style="margin:0;color:var(--eur)">€ EUR Gift Card</h2>
      <span class="tag eur">provider-specific</span>
    </div>
    <div class="muted" style="margin-bottom:0.5rem">Operator-issued, fiat-denominated voucher mint</div>
    <ul class="compare-list">
      <li class="yes">Stable EUR denomination — no volatility for the driver</li>
      <li class="yes">Same Cashu protocol, custom mint URL and unit</li>
      <li class="yes">Operator controls issuance, redemption, branding</li>
      <li class="yes">Reusable for loyalty, promo, and corporate-fleet vouchers</li>
      <li class="no">Trust sits with the issuing operator, not the Bitcoin network</li>
    </ul>
  </div>

  <!-- FOR CPOs -->
  <div class="panel full">
    <h2>For CPOs &amp; integration partners</h2>
    <p style="margin:0 0 0.5rem">
      Point any OCPI 2.2.1-compliant Charge Point Operator at our versions endpoint.
      We advertise the role <code>emsp</code> with party <code>NO/TGA</code>.
    </p>
    <code class="cpo-code">https://ocpi.nodns.shop/ocpi/versions</code>
    <div class="muted">
      Supported modules: <code>credentials</code>, <code>tokens</code> (incl. <code>/authorize</code>),
      <code>sessions</code>, <code>cdrs</code>, <code>locations</code>, <code>commands</code>.
      Handshake with Token A — <a href="/">see the dashboard</a> for live peer status.
    </div>
  </div>

  <!-- FOOTER CTA -->
  <div class="panel full footer">
    <div style="margin-bottom:0.5rem;color:var(--text);font-size:1rem">Ready to plug in?</div>
    <a class="cta" href="/">Open the dashboard →</a>
    <div style="margin-top:1.25rem">
      <a href="/">dashboard</a> ·
      <a href="/healthz">health</a> ·
      Party <code>NO/TGA</code> ·
      OCPI 2.2.1
    </div>
  </div>

</div>
</body>
</html>`
