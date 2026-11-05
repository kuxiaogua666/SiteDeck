"use strict";
const $ = (id) => document.getElementById(id);
const state = { view: "devices", csrf: "", data: { devices: [], grants: [], connections: [] }, busy: false, secret: null };
const views = {
  devices: { title: "设备列表", description: "查看现场设备状态，管理设备名称与接入策略。", table: "现场设备", search: "搜索设备名称或 ID", columns: ["设备", "状态", "运行环境", "现场能力", "连接数", "操作"], note: "设备名称与访问策略保存在当前 Hub。" },
  grants: { title: "访问授权", description: "签发有期限的访问凭据，精确控制可使用的现场能力。", table: "授权记录", search: "搜索授权名称或设备", columns: ["授权名称", "目标设备", "权限", "状态", "到期时间", "操作"], note: "授权密钥只在创建时提供；撤销会断开对应通道。" },
  connections: { title: "连接管理", description: "查看实际传输路径，管理正在建立或使用中的远程通道。", table: "活动连接", search: "搜索设备、连接或授权 ID", columns: ["目标设备 / 连接", "传输方式", "认证状态", "中继载荷", "建立时间", "操作"], note: "断开连接不会撤销授权，daemon 可能按现有授权自动重连。" }
};
const scopeNames = { host: "主机", ssh: "SSH", serial: "串口" };
function node(tag, className, text) { const n = document.createElement(tag); if (className) n.className = className; if (text !== undefined) n.textContent = text; return n; }
function cell(...children) { const td = node("td"); children.forEach(c => td.append(typeof c === "string" ? document.createTextNode(c) : c)); return td; }
function titleCell(title, subtitle, avatar) { const box = node("div", "device-cell"); if (avatar) box.append(node("span", "device-avatar", "▣")); const text = node("div"); text.append(node("span", "cell-title", title), node("span", "cell-sub", subtitle)); box.append(text); return cell(box); }
function badge(text, color = "") { return node("span", "badge " + color, text); }
function chips(scopes) { const box = node("div", "chips"); (scopes || []).forEach(s => box.append(node("span", "chip", scopeNames[s] || s))); if (!box.children.length) box.append(node("span", "cell-sub", "未开启")); return box; }
function action(text, handler, danger = false) { const b = node("button", "text-button" + (danger ? " destructive" : ""), text); b.type = "button"; b.addEventListener("click", handler); return b; }
function actions(...buttons) { const box = node("div", "cell-actions"); buttons.forEach(b => box.append(b)); return cell(box); }
function deviceName(id) { return state.data.devices.find(d => d.id === id)?.name || id; }
function date(text) { if (!text) return "—"; return new Date(text).toLocaleString("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }); }
function bytes(n) { if (!n) return "0 B"; const units = ["B", "KiB", "MiB", "GiB"]; const i = Math.min(3, Math.floor(Math.log(n) / Math.log(1024))); return (n / 1024 ** i).toFixed(i ? 1 : 0) + " " + units[i]; }
function active(g) { return !g.revokedAt && Date.parse(g.expiresAt) > Date.now(); }
function toast(text) { $("toast").textContent = text; $("toast").hidden = false; clearTimeout(toast.timer); toast.timer = setTimeout(() => $("toast").hidden = true, 3600); }
async function api(path, method = "GET", body) {
  const headers = {}; if (body !== undefined) headers["Content-Type"] = "application/json"; if (method !== "GET" && state.csrf) headers["X-CSRF-Token"] = state.csrf;
  const response = await fetch("/admin/api/" + path, { method, headers, credentials: "same-origin", body: body === undefined ? undefined : JSON.stringify(body), cache: "no-store" });
  let result; try { result = await response.json(); } catch { throw new Error("Hub 返回了无效响应，请检查连接。"); }
  if (!response.ok) { if (response.status === 401 && path !== "login") showLogin(); throw new Error(result.error?.message || "操作失败，请重试。"); }
  return result;
}
function showLogin() { state.csrf = ""; state.data = { devices: [], grants: [], connections: [] }; state.secret = null; document.querySelectorAll("dialog[open]").forEach(d => d.close()); $("grant-secret").textContent = ""; render(); $("app-view").hidden = true; $("login-view").hidden = false; }
async function enter(session) { state.csrf = session.csrfToken; $("app-version").textContent = "v" + session.version; $("login-secret").value = ""; $("login-file").value = ""; $("login-view").hidden = true; $("app-view").hidden = false; await refresh(); }
async function refresh() {
  if (!state.csrf || state.busy) return;
  state.busy = true;
  try { const session = state.csrf; const result = await api("overview"); if (session !== state.csrf) return; state.data = result; $("page-error").hidden = true; $("refresh-dot").className = "status-dot online"; $("refresh-status").textContent = "每 3 秒自动同步"; render(); }
  catch (error) { if (state.csrf) { $("page-error").textContent = error.message; $("page-error").hidden = false; $("refresh-dot").className = "status-dot error"; $("refresh-status").textContent = "连接暂时中断"; } }
  finally { state.busy = false; }
}
function switchView(view) {
  state.view = view; $("search").value = "";
  document.querySelectorAll(".nav-item").forEach(b => b.classList.toggle("active", b.dataset.view === view));
  const config = views[view]; $("page-title").textContent = config.title; $("breadcrumb-view").textContent = config.title; $("page-description").textContent = config.description; $("table-title").textContent = config.table; $("search").placeholder = config.search; $("table-note").textContent = config.note;
  $("status-filter").replaceChildren(); const options = view === "devices" ? [["all", "全部状态"], ["online", "仅在线"], ["offline", "仅离线"]] : view === "grants" ? [["all", "全部状态"], ["active", "有效授权"], ["expired", "已过期"], ["revoked", "已撤销"]] : [["all", "全部路径"], ["p2p", "P2P 直连"], ["relay", "Hub 中继"], ["turn", "TURN 中继"]];
  options.forEach(([value, text]) => { const option = node("option", "", text); option.value = value; $("status-filter").append(option); }); render();
}
function render() {
  const { devices, grants, connections } = state.data; const online = devices.filter(d => d.online).length;
  $("stat-online").textContent = online; $("stat-devices-note").textContent = "已登记 " + devices.length + " 台设备 · 离线 " + (devices.length - online) + " 台";
  $("stat-connections").textContent = connections.length; $("stat-grants").textContent = grants.filter(active).length; $("stat-bytes").textContent = bytes(connections.reduce((sum, c) => sum + c.relayBytes, 0));
  $("nav-devices").textContent = devices.length; $("nav-grants").textContent = grants.filter(active).length; $("nav-connections").textContent = connections.length;
  $("updated-at").textContent = "最近同步 " + date(state.data.asOf); $("new-grant").disabled = !devices.length;
  const config = views[state.view]; const heading = node("tr"); config.columns.forEach(text => heading.append(node("th", "", text))); $("table-head").replaceChildren(heading); $("table-body").replaceChildren();
  const query = $("search").value.trim().toLowerCase(), filter = $("status-filter").value;
  let rows;
  if (state.view === "devices") rows = devices.filter(d => (d.name + " " + d.id).toLowerCase().includes(query) && (filter === "all" || (filter === "online" ? d.online : !d.online)));
  else if (state.view === "grants") rows = grants.filter(g => (g.name + " " + deviceName(g.deviceId)).toLowerCase().includes(query) && (filter === "all" || (filter === "active" ? active(g) : filter === "revoked" ? !!g.revokedAt : !g.revokedAt && !active(g))));
  else rows = connections.filter(c => (c.id + " " + c.deviceName + " " + (c.grantId || "")).toLowerCase().includes(query) && (filter === "all" || c.transport === filter));
  rows.forEach(item => { const tr = node("tr"); if (state.view === "devices") renderDevice(tr, item); else if (state.view === "grants") renderGrant(tr, item); else renderConnection(tr, item); $("table-body").append(tr); });
  $("table-count").textContent = rows.length; $("empty-state").hidden = rows.length > 0; $("table-head").hidden = !rows.length;
  if (!rows.length) { const searching = !!query || filter !== "all"; $("empty-title").textContent = searching ? "没有匹配的结果" : state.view === "devices" ? "还没有设备接入" : state.view === "grants" ? "还没有访问授权" : "当前没有活动连接"; $("empty-text").textContent = searching ? "试试其他关键词，或调整筛选条件。" : state.view === "devices" ? "启动现场 Agent 并连接到此 Hub，设备将在这里自动出现。" : state.view === "grants" ? "为现场设备创建有期限的访问授权，并按需开放能力。" : "工程师 CLI 建立连接后，会在这里显示实际传输方式。"; }
}
function renderDevice(tr, d) {
  const status = node("div", "cell-stack"); status.append(badge(d.online ? "● 在线" : "○ 离线", d.online ? "green" : "")); if (d.blocked) status.append(badge("访问已阻止", "red"));
  const platform = node("div", "cell-stack"); platform.append(node("span", "mono", (d.os || "未知系统") + " / " + (d.arch || "—")), node("span", "cell-sub", d.version ? "Agent v" + d.version : "未报告版本"));
  tr.append(titleCell(d.name, d.id, true), cell(status), cell(platform), cell(chips(d.capabilities)), cell(node("span", "mono", String(d.connections))), actions(action("重命名", () => renameDevice(d)), action("访问策略", () => devicePolicy(d)), action("授权", () => newGrant(d.id))));
}
function renderGrant(tr, g) { const status = g.revokedAt ? badge("已撤销", "red") : active(g) ? badge("有效", "green") : badge("已过期", "amber"); tr.append(titleCell(g.name, g.id), cell(node("span", "cell-title", deviceName(g.deviceId))), cell(chips(g.scopes)), cell(status), cell(node("span", "mono", date(g.expiresAt))), actions(...(g.revokedAt ? [node("span", "cell-sub", "—")] : [action("撤销授权", () => confirmAction("撤销访问授权", "撤销后将断开使用「" + g.name + "」的现有连接，并拒绝用该授权再次连接。", "确认撤销", () => api("grants/" + encodeURIComponent(g.id), "DELETE")), true)]))); }
function renderConnection(tr, c) { const transport = { p2p: ["P2P 直连", "green"], relay: ["Hub 中继", "blue"], turn: ["TURN 中继", "amber"], negotiating: ["正在协商", ""] }[c.transport] || [c.transport, ""]; const connection = titleCell(c.deviceName, c.id, true); const auth = node("div", "cell-stack"); auth.append(badge(c.authenticatedAt ? "已认证" : "等待认证", c.authenticatedAt ? "green" : "amber"), node("span", "cell-sub", c.grantId ? "Hub 授权 + 配对" : "设备配对")); tr.append(connection, cell(badge(...transport)), cell(auth), cell(node("span", "mono", bytes(c.relayBytes))), cell(node("span", "mono", date(c.createdAt))), actions(action("断开连接", () => confirmAction("断开当前连接", "这会断开「" + c.deviceName + "」的当前通道。若客户端运行 daemon，它可能自动重连。要禁止再次连接，请撤销授权或阻止设备访问。", "确认断开", () => api("connections/" + encodeURIComponent(c.id), "DELETE")), true))); }
function modal(id) { $(id).querySelectorAll(".form-error").forEach(n => n.textContent = ""); $(id).showModal(); }
function renameDevice(d) { $("rename-form").dataset.id = d.id; $("rename-device-id").textContent = d.id; $("device-name").value = d.name; modal("rename-dialog"); $("device-name").focus(); }
function devicePolicy(d) { $("policy-form").dataset.id = d.id; $("policy-name").textContent = d.name + " · 访问策略"; $("paired-policy").value = d.allowPaired ? "paired" : "grant"; $("device-blocked").checked = d.blocked; modal("policy-dialog"); }
async function submitForm(event, errorID, operation) { event.preventDefault(); const button = event.target.querySelector("button[type=submit]"); if (button) button.disabled = true; $(errorID).textContent = ""; try { await operation(); } catch (error) { $(errorID).textContent = error.message; } finally { if (button) button.disabled = false; } }
function newGrant(id) { $("grant-form").reset(); $("grant-device").replaceChildren(); state.data.devices.forEach(d => { const option = node("option", "", d.name + (d.online ? " · 在线" : " · 离线")); option.value = d.id; $("grant-device").append(option); }); if (id) $("grant-device").value = id; $("grant-expiry").hidden = true; updateScopes(); modal("grant-dialog"); }
function updateScopes() { const device = state.data.devices.find(d => d.id === $("grant-device").value); document.querySelectorAll('input[name="scope"]').forEach(input => { const available = device?.capabilities?.includes(input.value); input.disabled = !available; input.checked = !!available; }); $("scope-note").textContent = device?.capabilities?.length ? "可选择该设备已开启的能力。现场关闭能力后，授权也无法绕过。" : "现场未报告已开启的能力，请先在 Agent 开启所需功能。"; }
let confirmCallback;
function confirmAction(title, description, button, callback) { $("confirm-title").textContent = title; $("confirm-description").textContent = description; $("confirm-action").textContent = button; confirmCallback = callback; modal("confirm-dialog"); }
$("confirm-action").addEventListener("click", async () => { $("confirm-action").disabled = true; try { await confirmCallback(); $("confirm-dialog").close(); toast("操作已生效"); await refresh(); } catch (error) { $("confirm-error").textContent = error.message; } finally { $("confirm-action").disabled = false; } });
$("login-form").addEventListener("submit", event => submitForm(event, "login-error", async () => enter(await api("login", "POST", { secret: $("login-secret").value.trim() }))));
$("login-file").addEventListener("change", async event => { const file = event.target.files[0]; if (!file) return; if (file.size > 4096) { $("login-error").textContent = "密钥文件过大，请选择 hub-admin.key。"; return; } $("login-secret").value = (await file.text()).trim(); });
$("rename-form").addEventListener("submit", event => submitForm(event, "rename-error", async () => { await api("devices/" + encodeURIComponent(event.target.dataset.id), "PATCH", { name: $("device-name").value.trim() }); $("rename-dialog").close(); toast("设备名称已保存"); await refresh(); }));
$("policy-form").addEventListener("submit", event => submitForm(event, "policy-error", async () => { await api("devices/" + encodeURIComponent(event.target.dataset.id), "PATCH", { allowPaired: $("paired-policy").value === "paired", blocked: $("device-blocked").checked }); $("policy-dialog").close(); toast("访问策略已更新"); await refresh(); }));
$("grant-form").addEventListener("submit", event => submitForm(event, "grant-error", async () => { const scopes = [...document.querySelectorAll('input[name="scope"]:checked')].map(n => n.value); if (!scopes.length) throw new Error("请至少选择一项已开启的能力。"); const ttl = $("grant-ttl").value; let expires; if (ttl === "custom") { if (!$("grant-expiry").value) throw new Error("请选择到期时间。"); expires = new Date($("grant-expiry").value); } else expires = new Date(Date.now() + Number(ttl) * 3600000); const result = await api("grants", "POST", { deviceId: $("grant-device").value, name: $("grant-name").value.trim(), scopes, expiresAt: expires.toISOString(), requireGrant: $("grant-require").checked }); state.secret = result; $("grant-secret").textContent = result.accessToken; $("grant-command").textContent = "site-cli --profile pair.json --access-file hub-access-" + result.grant.id + ".json doctor"; $("grant-dialog").close(); modal("secret-dialog"); await refresh(); }));
$("grant-device").addEventListener("change", updateScopes); $("grant-ttl").addEventListener("change", () => $("grant-expiry").hidden = $("grant-ttl").value !== "custom");
$("copy-secret").addEventListener("click", async () => { if (!state.secret) return; try { await navigator.clipboard.writeText(state.secret.accessToken); toast("密钥已复制"); } catch { toast("无法自动复制，请下载授权文件，或选中密钥复制。"); } });
$("download-grant").addEventListener("click", () => { if (!state.secret) return; const { grant, accessToken } = state.secret; const data = JSON.stringify({ deviceId: grant.deviceId, accessToken, grantId: grant.id, scopes: grant.scopes, expiresAt: grant.expiresAt }, null, 2); const url = URL.createObjectURL(new Blob([data + "\n"], { type: "application/json" })); const a = node("a"); a.href = url; a.download = "hub-access-" + grant.id + ".json"; document.body.append(a); a.click(); a.remove(); setTimeout(() => URL.revokeObjectURL(url), 1000); toast("授权文件已下载"); });
$("secret-dialog").addEventListener("close", () => { state.secret = null; $("grant-secret").textContent = ""; $("grant-command").textContent = ""; });
document.querySelectorAll("[data-close]").forEach(button => button.addEventListener("click", () => $(button.dataset.close).close()));
document.querySelectorAll(".nav-item").forEach(button => button.addEventListener("click", () => switchView(button.dataset.view)));
$("search").addEventListener("input", render); $("status-filter").addEventListener("change", render); $("refresh").addEventListener("click", refresh); $("new-grant").addEventListener("click", () => newGrant());
$("logout").addEventListener("click", async () => { try { await api("logout", "POST", {}); showLogin(); } catch (error) { toast(error.message); } });
setInterval(refresh, 3000);
(async () => { try { await enter(await api("session")); } catch { showLogin(); } })();
