(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  let auth = window.__AGENT_SESSION__ || null;
  delete window.__AGENT_SESSION__;
  let info = null, generation = null, kind = 'host', pollTimer, requestID = 0, activeTab;
  const tabs = new Map(), networkTabs = new Map();
 const {DataPanel, decode64} = window.SiteDebug;
  const labels = {online:'Hub 已连接', local:'本机模式', stopped:'已停止', starting:'正在启动', stopping:'正在停止', connecting:'正在连接 Hub', reconnecting:'正在重连', error:'启动失败'};
  const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
  function ask(text) {
    return new Promise(resolve => {
      $('confirm-text').textContent = text;
      const finish = result => { $('confirm-dialog').close(); resolve(result); };
      $('confirm-yes').onclick = () => finish(true);
      $('confirm-no').onclick = () => finish(false);
      $('confirm-dialog').oncancel = event => { event.preventDefault(); finish(false); };
      $('confirm-dialog').showModal();
    });
  }
  function message(text, ok = false) { $('notice').textContent = text; $('notice').classList.toggle('success', ok); $('notice').hidden = !text; }
  async function api(path, body, raw = false) {
    const headers = {};
    if (auth) { headers.Authorization = 'Bearer ' + auth.token; headers['X-CSRF-Token'] = auth.csrfToken; }
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    const response = await fetch(path, {method:body === undefined ? 'GET' : 'POST', headers, body:body === undefined ? undefined : JSON.stringify(body), credentials:'omit', cache:'no-store'});
    if (response.status === 401 && path !== '/api/login') { auth = null; showLogin(); }
    if (!response.ok) { const value = await response.json().catch(() => ({})); throw new Error(value.error?.message || '请求失败：' + response.status); }
    return raw ? response.blob() : response.json();
  }
  async function rpc(method, params = {}, timeoutMs = 30000) {
    const result = await api('/api/rpc', {id:String(++requestID), method, params, timeoutMs});
    if (result.error) throw new Error(result.error.message);
    return result.result;
  }
  function showLogin() { clearTimeout(pollTimer); disposeTabs(); disposeNetworks(); document.querySelectorAll('dialog[open]').forEach(node => node.close()); $('application').hidden = true; $('login').hidden = false; }
  async function enter() { $('login').hidden = true; $('application').hidden = false; await refresh(); fillSettings(); poll(); }
  $('login-file').addEventListener('change', async event => {
    try { const file = event.target.files[0]; if (!file || file.size > 8192) throw new Error('请选择本机 ui-access.key 文件'); auth = await api('/api/login', {token:(await file.text()).trim()}); await enter(); }
    catch (error) { $('login-error').textContent = error.message; } finally { event.target.value = ''; }
  });
  $('login-form').onsubmit = async event => {
    event.preventDefault();
    try { auth = await api('/api/login', {token:$('login-token').value.trim()}); $('login-token').value = ''; await enter(); }
    catch (error) { $('login-error').textContent = error.message; }
  };
  document.querySelector('.brand').onclick = event => { event.preventDefault(); page('workspace'); };
  function page(name) {
    for (const node of document.querySelectorAll('.page')) node.hidden = node.id !== name;
    for (const node of document.querySelectorAll('.nav')) node.classList.toggle('selected', node.dataset.page === name);
    $('page-title').textContent = {workspace:'终端', network:'网络调试', overview:'设备与连接', settings:'连接设置'}[name];
    if (name === 'workspace' && activeTab) requestAnimationFrame(() => fit(tabs.get(activeTab)));
  }
  document.querySelectorAll('.nav').forEach(node => node.addEventListener('click', () => page(node.dataset.page)));
  $('configure').onclick = () => page('settings');
  function fillSettings() {
    if (!info) return;
    const s = info.settings;
    $('configuration-path').textContent = '当前配置文件：' + info.configPath;
    $('setting-name').value = s.name;
    $('setting-hub').value = s.hub;
    $('setting-remote').checked = s.remoteEnabled;
    $('setting-autostart').checked = s.autoStart;
    $('setting-ttl').value = s.guestTTLHours;
    $('setting-ice').value = s.iceFile || '';
    $('setting-registration').value = '';
    document.querySelectorAll('[name=permission]').forEach(node => { node.checked = s.permissions.includes(node.value); });
    $('registration-state').textContent = s.registrationConfigured ? '已保存注册凭据。留空将保留。' : '尚未导入注册凭据';
  }
  $('registration-file').onchange = async event => {
    try { const file = event.target.files[0]; if (!file || file.size > 4098) throw new Error('注册凭据文件过大'); $('setting-registration').value = (await file.text()).trim(); $('registration-state').textContent = '已读取文件，保存后生效'; }
    catch (error) { message(error.message); } finally { event.target.value = ''; }
  };
  $('settings-form').onsubmit = async event => {
    event.preventDefault(); const button = event.submitter; button.disabled = true;
    try {
      const settings = {version:1, name:$('setting-name').value.trim(), hub:$('setting-hub').value.trim(), remoteEnabled:$('setting-remote').checked, permissions:[...document.querySelectorAll('[name=permission]:checked')].map(n => n.value), guestTTLHours:Number($('setting-ttl').value), autoStart:$('setting-autostart').checked, registrationToken:$('setting-registration').value.trim(), iceFile:$('setting-ice').value.trim()};
      render(await api('/api/config', {settings, restart:true})); fillSettings(); message('配置已保存并应用。', true);
    } catch (error) { message(error.message); } finally { button.disabled = false; }
  };
  async function action(name) { try { render(await api('/api/' + name, {})); message(name === 'start' ? '现场服务已启动。' : name === 'stop' ? '现场服务已停止。' : '秘钥已轮换，旧配对文件已失效。', true); } catch (error) { message(error.message); } }
  $('start').onclick = () => action('start'); $('stop').onclick = () => action('stop');
  $('renew').onclick = async () => { if (await ask('轮换将断开原连接，并使旧配对文件失效。继续？')) action('renew'); };
  $('quit').onclick = async () => { if (!await ask('退出程序并停止现场服务？')) return; try { await api('/api/shutdown', {}); clearTimeout(pollTimer); disposeTabs(); disposeNetworks(); message('程序已退出，可以关闭此窗口。', true); } catch (error) { message(error.message); } };
  async function copyPairText() {
    try { await navigator.clipboard.writeText($('pair-text-value').value); message('配对文本已复制，可以直接交给连接端。', true); }
    catch (_) { $('pair-text-value').focus(); $('pair-text-value').select(); message('请按 Ctrl+C 复制已选中的配对文本。'); }
  }
  $('pair-text').onclick = async () => {
    try { const value = await api('/api/pair-text'); $('pair-text-value').value = value.pairingText; $('pair-text-dialog').showModal(); await copyPairText(); }
    catch (error) { message(error.message); }
  };
  $('pair-text-copy').onclick = copyPairText;
  $('pair-text-close').onclick = () => $('pair-text-dialog').close();
  $('pair-text-dialog').addEventListener('close', () => { $('pair-text-value').value = ''; });
  $('pair').onclick = async () => { try { const blob = await api('/api/pair', undefined, true), url = URL.createObjectURL(blob), anchor = document.createElement('a'); anchor.href = url; anchor.download = 'pair.json'; anchor.click(); setTimeout(() => URL.revokeObjectURL(url), 10000); message('已请求保存配对文件，请妥善保管。', true); } catch (error) { message(error.message); } };
  function render(value) {
    info = value;
    if (generation !== null && generation !== value.generation) disposeTabs();
    generation = value.generation;
    $('status').textContent = labels[value.state] || value.state; $('status').className = 'pill ' + value.state;
    $('start').disabled = value.running; $('stop').disabled = !value.running;
    $('aside-name').textContent = value.settings.name; $('platform').textContent = value.os + ' / ' + value.arch;
    $('version').textContent = 'SiteDeck · ' + value.version;
    $('device-name').textContent = value.settings.name; $('device-id').textContent = value.deviceId;
    $('fingerprint').textContent = value.fingerprint; $('directory').textContent = value.dataDirectory;
    $('expiry').textContent = new Date(value.guestExpiresAt).toLocaleString() + (value.guestExpired ? ' · 已到期' : '');
    $('channels').replaceChildren();
    for (const ch of value.channels) { const node = document.createElement('div'); node.className = 'channel'; node.textContent = (ch.transport || '已连接') + ' · ' + ch.channelId; $('channels').append(node); }
    if (!value.channels.length) $('channels').textContent = value.lastError || '暂无远程连接';
    $('events').replaceChildren();
    for (const event of [...value.events].reverse()) { const row = document.createElement('div'), time = document.createElement('time'), text = document.createElement('span'); row.className = 'event'; time.textContent = new Date(event.time).toLocaleTimeString(); text.textContent = event.message || labels[event.state] || event.type; row.append(time, text); $('events').append(row); }
    $('session-list').replaceChildren();
    for (const session of value.sessions) {
      const node = document.createElement('div'), title = document.createElement('strong'), details = document.createElement('small'), actions = document.createElement('div');
      node.className = 'session-item'; title.textContent = session.name; details.textContent = session.kind + ' · ' + session.id.slice(0, 10); actions.className = 'actions';
      for (const readOnly of [false, true]) { const join = document.createElement('button'); join.textContent = readOnly ? '只读加入' : '加入'; join.onclick = () => attach(session, readOnly).catch(e => message(e.message)); actions.append(join); }
      const close = document.createElement('button'); close.textContent = '关闭会话'; close.onclick = async () => { if (!await ask('关闭会话会影响所有连接者，继续？')) return; try { await rpc('session.close', {id:session.id}); if (tabs.has(session.id)) removeTab(session.id); await refresh(); } catch (error) { message(error.message); } }; actions.append(close); node.append(title, details, actions); $('session-list').append(node);
    }
    if (!value.sessions.length) $('session-list').textContent = '暂无活动会话';
    for (const type of ['host','ssh','serial']) $('new-' + type).disabled = !value.ready || !value.settings.permissions.includes(type);
  }
  async function refresh() { render(await api('/api/status')); }
  async function poll() { if (!auth) return; try { await refresh(); } catch (error) { message('无法读取现场状态：' + error.message); } if (auth) pollTimer = setTimeout(poll, 2000); }
  function openDialog(type) {
    kind = type; $('connect-error').textContent = '';
    for (const key of ['host','ssh','serial']) $(key + '-fields').hidden = key !== type;
    $('connect-title').textContent = {host:'新建本机终端', ssh:'连接 SSH 主机', serial:'连接串口'}[type];
    $('connect-dialog').showModal(); if (type === 'serial') refreshPorts();
  }
  for (const type of ['host','ssh','serial']) $('new-' + type).onclick = () => openDialog(type);
  $('dialog-close').onclick = () => $('connect-dialog').close();
  $('connect-dialog').addEventListener('close', () => { $('ssh-password').value = ''; $('ssh-passphrase').value = ''; $('ssh-key').value = ''; });
  for (const id of ['ssh-host','ssh-port','ssh-fingerprint']) $(id).oninput = () => { $('ssh-verified').checked = false; };
  $('scan-ssh').onclick = async event => { event.target.disabled = true; try { const result = await rpc('ssh.hostkey.scan', {host:$('ssh-host').value.trim(), port:Number($('ssh-port').value)}); $('ssh-fingerprint').value = result.fingerprint; $('ssh-verified').checked = false; $('connect-error').textContent = '已读取指纹，请通过可信渠道核对后再连接。'; } catch (error) { $('connect-error').textContent = error.message; } finally { event.target.disabled = false; } };
  async function refreshPorts() { try { const result = await api('/api/serial-ports'); $('port-options').replaceChildren(); for (const item of result.ports) { const option = document.createElement('option'); option.value = typeof item === 'string' ? item : item.name || item.port; $('port-options').append(option); } } catch (error) { $('connect-error').textContent = error.message; } }
  $('refresh-ports').onclick = refreshPorts;
  $('connect-form').onsubmit = async event => {
    event.preventDefault(); event.submitter.disabled = true;
    try {
      let params, method;
      if (kind === 'host') { method = 'host.shell'; params = {shell:$('host-shell').value, cwd:$('host-cwd').value, rows:24, cols:100}; }
      if (kind === 'ssh') {
        if (!$('ssh-verified').checked) throw new Error('请先核对并确认 SSH 主机指纹');
        const key = $('ssh-key').files[0]; if (key && key.size > 48 * 1024) throw new Error('私钥文件超过大小限制');
        method = 'ssh.shell'; params = {host:$('ssh-host').value.trim(), port:Number($('ssh-port').value), user:$('ssh-user').value, password:$('ssh-password').value, privateKey:key ? await key.text() : '', passphrase:$('ssh-passphrase').value, fingerprint:$('ssh-fingerprint').value.trim(), rows:24, cols:100};
      }
      if (kind === 'serial') { method = 'serial.connect'; params = {port:$('serial-port').value.trim(), baudRate:Number($('serial-baud').value), dataBits:Number($('serial-bits').value), stopBits:Number($('serial-stop').value), parity:$('serial-parity').value}; }
      const session = await rpc(method, params);
      try { await attach(session, false); } catch (error) { await rpc('session.close', {id:session.id}).catch(() => {}); throw error; }
      $('connect-dialog').close(); await refresh();
    } catch (error) { $('connect-error').textContent = error.message; } finally { event.submitter.disabled = false; }
  };
  async function attach(session, readOnly) {
    if (tabs.has(session.id)) { activate(session.id); return; }
    if (tabs.size >= 12) throw new Error('请先关闭部分终端标签页（最多 12 个）');
    await rpc('session.attach', {id:session.id, readOnly});
    const pane = document.createElement('div'), tab = document.createElement('div'), title = document.createElement('button'), close = document.createElement('button');
    pane.className = 'terminal-pane'; pane.hidden = true; tab.className = 'tab'; tab.setAttribute('role','tab');
    title.textContent = session.name + (readOnly ? ' · 只读' : ''); title.onclick = () => activate(session.id); close.textContent = '×'; close.className = 'close-tab'; close.setAttribute('aria-label','关闭终端标签页');
    tab.append(title, close); $('tabs').append(tab); $('terminals').append(pane);
    const term = new Terminal({cursorBlink:!readOnly, disableStdin:readOnly, fontFamily:'Consolas, "Cascadia Mono", monospace', fontSize:13, scrollback:5000, theme:{background:'#090e15', foreground:'#d8e3ef', cursor:'#7bd8b1', selectionBackground:'#405b77'}}), fitAddon = new FitAddon.FitAddon();
    const terminalHost = document.createElement('div'); terminalHost.className = 'terminal-display'; pane.append(terminalHost);
    term.loadAddon(fitAddon); term.open(terminalHost);
    const view = {id:session.id, kind:session.kind, terminalHost, term, fitAddon, pane, tab, readOnly, closed:false, cursor:'', writeChain:Promise.resolve()}; tabs.set(session.id, view);
    if (session.kind === 'serial') {
      pane.classList.add('serial-pane');
      const modebar = document.createElement('div'), toggle = document.createElement('button'), hint = document.createElement('small'), debugRoot = document.createElement('div');
      modebar.className = 'serial-modebar'; toggle.textContent = '切换到交互终端'; hint.textContent = '串口助手 · 收发记录保留原始字节'; modebar.append(hint, toggle); pane.prepend(modebar); pane.append(debugRoot);
      terminalHost.hidden = true;
      view.debug = new DataPanel(debugRoot, {readOnly, send:data => rpc('session.write', {id:view.id, data})});
      toggle.onclick = () => { terminalHost.hidden = !terminalHost.hidden; debugRoot.hidden = !terminalHost.hidden; toggle.textContent = terminalHost.hidden ? '切换到交互终端' : '切换到串口助手'; if (!terminalHost.hidden) { fit(view); term.focus(); } };
    }
    close.onclick = async () => { try { await rpc('session.detach', {id:session.id}); removeTab(session.id); await refresh(); } catch (error) { message(error.message); } };
    term.onData(text => { view.writeChain = view.writeChain.then(async () => { if (view.closed || view.terminalHost.hidden) return; await rpc('session.write', {id:view.id, text}); }).catch(error => { term.write('\r\n\x1b[31m[输入失败：' + error.message + ']\x1b[0m\r\n'); }); });
    term.onResize(size => { if (!readOnly && session.kind !== 'serial' && !view.closed) rpc('session.resize', {id:view.id, rows:size.rows, cols:size.cols}).catch(() => {}); });
    page('workspace'); activate(session.id); read(view);
  }
  function fit(view) { if (!view || view.closed || view.pane.hidden || view.terminalHost.hidden) return; try { view.fitAddon.fit(); } catch (_) {} }
  function activate(id) { activeTab = id; for (const [key, view] of tabs) { view.pane.hidden = key !== id; view.tab.classList.toggle('active', key === id); view.tab.setAttribute('aria-selected', String(key === id)); } $('terminal-empty').hidden = tabs.size > 0; requestAnimationFrame(() => { fit(tabs.get(id)); if (!tabs.get(id)?.terminalHost.hidden) tabs.get(id)?.term.focus(); }); }
  function removeTab(id) { const view = tabs.get(id); if (!view) return; view.closed = true; view.debug?.dispose(); view.term.dispose(); view.pane.remove(); view.tab.remove(); tabs.delete(id); if (activeTab === id) activate(tabs.keys().next().value); $('terminal-empty').hidden = tabs.size > 0; }
  function disposeTabs() { for (const id of [...tabs.keys()]) removeTab(id); }
  async function read(view) {
    try {
      while (!view.closed && auth) {
        const result = await rpc('session.read', {id:view.id, cursor:view.cursor, waitMs:1000, limit:49152, source:'all'}, 5000);
        if (view.closed) break;
        view.cursor = result.nextCursor;
        if (result.overflow) { view.term.write('\r\n[缓冲区已覆盖部分历史输出]\r\n'); view.debug?.note('缓冲区已覆盖部分历史输出'); }
        if (view.debug && result.events) { for (const event of result.events) { view.debug.add(event); if (event.direction === 'rx') view.term.write(decode64(event.data)); } }
        else if (result.data) view.term.write(decode64(result.data));
        if (result.eof) { view.debug?.disable('会话已结束'); view.term.write('\r\n[会话已结束，退出码 ' + result.exitCode + ']\r\n'); break; }
        await sleep(20);
      }
    } catch (error) { if (!view.closed) { view.debug?.disable('读取已停止：' + error.message); view.term.write('\r\n[读取已停止：' + error.message + ']\r\n'); } }
  }

  function disposeNetworks() { for (const view of networkTabs.values()) { view.closed = true; view.panel.dispose(); view.pane.remove(); view.tab.remove(); } networkTabs.clear(); $('network-empty').hidden = false; }
  function activateNetwork(id) { for (const [key, view] of networkTabs) { view.pane.hidden = key !== id; view.tab.classList.toggle('active', key === id); view.tab.setAttribute('aria-selected', String(key === id)); } }
  $('new-network').onclick = () => { $('network-connect-error').textContent = ''; $('network-dialog').showModal(); };
  $('network-dialog-close').onclick = () => $('network-dialog').close();
  $('network-mode').onchange = () => {
    const mode = $('network-mode').value;
    $('network-local-field').hidden = mode === 'tcp-client'; $('network-remote-field').hidden = mode === 'tcp-server';
    $('network-local').value = mode === 'udp' ? '127.0.0.1:48772' : '127.0.0.1:48771';
    $('network-mode-hint').textContent = mode === 'tcp-client' ? '填写服务端 IP 或主机名与端口，例如 192.168.1.100:48771。' : '本地地址填本机网卡 IP 或 0.0.0.0 可供局域网设备连接；端口填 0 自动分配。';
  };
  $('network-form').onsubmit = async event => {
    event.preventDefault(); event.submitter.disabled = true;
    try {
      const mode = $('network-mode').value;
      const result = await api('/api/network/open', {mode, local:mode === 'tcp-client' ? '' : $('network-local').value.trim(), remote:mode === 'tcp-server' ? '' : $('network-remote').value.trim()});
      addNetwork(result); $('network-dialog').close(); page('network');
    } catch (error) { $('network-connect-error').textContent = error.message; }
    finally { event.submitter.disabled = false; }
  };
  function addNetwork(result) {
    const pane = document.createElement('div'), tab = document.createElement('div'), title = document.createElement('button'), close = document.createElement('button'), address = document.createElement('div'), peers = document.createElement('select'), disconnect = document.createElement('button'), remote = document.createElement('input'), log = document.createElement('div');
    pane.className = 'network-pane'; tab.className = 'tab'; tab.setAttribute('role','tab'); title.textContent = result.mode.toUpperCase() + ' · ' + result.local; close.textContent = '×'; close.setAttribute('aria-label','关闭网络会话'); tab.append(title, close);
    address.className = 'network-address mono';
    const controls = document.createElement('div'); controls.className = 'network-controls';
    peers.setAttribute('aria-label','发送目标连接'); disconnect.textContent = '断开所选连接'; remote.setAttribute('aria-label','UDP 目标地址'); remote.placeholder = '目标地址，例如 192.168.1.100:48772'; remote.value = result.remote;
    if (result.mode === 'udp') controls.append(remote); else controls.append(peers);
    if (result.mode === 'tcp-server') controls.append(disconnect);
    pane.append(address, controls, log); $('network-panes').append(pane); $('network-tabs').append(tab); $('network-empty').hidden = true;
    const view = {id:result.id, pane, tab, closed:false, cursor:0, peerSignature:null}; networkTabs.set(view.id, view);
    view.panel = new DataPanel(log, {send:data => api('/api/network/send', {id:view.id, peer:peers.value, remote:remote.value.trim(), data})});
    title.onclick = () => activateNetwork(view.id);
    disconnect.onclick = async () => { try { if (!peers.value) throw new Error('请先选择一个客户端连接'); await api('/api/network/disconnect', {id:view.id, peer:peers.value}); } catch (error) { view.panel.fail(error); } };
    close.onclick = async () => {
      try {
        view.panel.stopRepeat(); await api('/api/network/close', {id:view.id}); view.closed = true; view.panel.dispose(); pane.remove(); tab.remove(); networkTabs.delete(view.id); activateNetwork(networkTabs.keys().next().value); $('network-empty').hidden = networkTabs.size > 0;
      } catch (error) { view.panel.fail(error); }
    };
    function update(value) {
      address.textContent = (value.closed ? '已关闭' : value.mode === 'tcp-client' ? '已连接' : '监听中') + ' · 本地 ' + value.local + (value.remote ? ' → ' + value.remote : '');
      const signature = JSON.stringify(value.peers);
      if (signature !== view.peerSignature) {
        const selected = peers.value; peers.replaceChildren();
        if (value.mode === 'tcp-server') { const all = document.createElement('option'); all.value = ''; all.textContent = '广播到所有客户端（' + value.peers.length + '）'; peers.append(all); }
        for (const peer of value.peers) { const option = document.createElement('option'); option.value = peer.id; option.textContent = peer.address + ' · ' + peer.id; peers.append(option); }
        if ([...peers.options].some(option => option.value === selected)) peers.value = selected;
        view.peerSignature = signature;
      }
      disconnect.disabled = !peers.value; peers.onchange = () => { disconnect.disabled = !peers.value; };
      if (value.overflow) view.panel.note('缓冲区已覆盖部分历史数据');
      for (const entry of value.events) view.panel.add(entry);
      view.cursor = value.nextCursor;
      if (value.closed) view.panel.disable('连接已关闭，可以保存记录后关闭标签页');
    }
    update(result); activateNetwork(view.id);
    (async () => {
      try { while (!view.closed && auth) { const value = await api('/api/network/read?id=' + encodeURIComponent(view.id) + '&cursor=' + view.cursor); if (view.closed) break; update(value); if (value.closed) break; await sleep(300); } }
      catch (error) { if (!view.closed) view.panel.disable('读取已停止：' + error.message); }
    })();
  }
  window.addEventListener('resize', () => fit(tabs.get(activeTab)));
  if (auth) enter().catch(error => { message(error.message); showLogin(); }); else showLogin();
})();
