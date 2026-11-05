/* Raw bytes remain unchanged when switching display modes. */
(() => {
  'use strict';
  const decode64 = text => Uint8Array.from(atob(text || ''), c => c.charCodeAt(0));
  const encode64 = bytes => { let value = ''; for (const b of bytes) value += String.fromCharCode(b); return btoa(value); };
  function parseSend(text, hex, ending) {
    let bytes;
    if (hex) {
      const value = text.replace(/\s/g, '');
      if (!value || value.length % 2 || !/^[0-9a-f]+$/i.test(value)) throw new Error('HEX 请填写完整字节，例如 01 03 00 FF；每个字节需要两位十六进制数');
      bytes = Uint8Array.from(value.match(/../g), item => parseInt(item, 16));
    } else bytes = new TextEncoder().encode(text);
    const suffix = new TextEncoder().encode(ending);
    const result = new Uint8Array(bytes.length + suffix.length); result.set(bytes); result.set(suffix, bytes.length);
    if (!result.length) throw new Error('请输入发送内容');
    if (result.length > 32768) throw new Error('单次发送最多 32 KiB');
    return result;
  }
  class DataPanel {
    constructor(root, options) {
      this.root = root; this.options = options; this.records = []; this.size = 0; this.rx = 0; this.tx = 0; this.disposed = false; this.timer = null; this.sending = false; this.renderTimer = null;
      root.className = 'data-panel';
      root.innerHTML = `<div class="data-options"><label class="check"><input type="checkbox" data-ui="hex-display">HEX 显示</label><label class="check"><input type="checkbox" data-ui="timestamp" checked>时间戳</label><label class="check"><input type="checkbox" data-ui="autoscroll" checked>自动滚动</label><span data-ui="counts" class="mono">RX 0 B · TX 0 B</span><button data-ui="clear" type="button">清空</button><button data-ui="save" type="button">保存记录</button></div><pre class="data-log" data-ui="log" aria-label="收发记录" tabindex="0"></pre><div data-ui="error" class="debug-error" role="status"></div><div class="data-send"><textarea data-ui="text" rows="2" aria-label="发送内容" placeholder="输入发送内容；HEX 示例：01 03 00 FF" spellcheck="false"></textarea><div class="send-options"><label class="check"><input data-ui="hex-send" type="checkbox">HEX 发送</label><label class="compact">追加<select data-ui="ending"><option value="">无换行</option value="crlf">CRLF</option><option value="lf">LF</option><option value="cr">CR</option></select></label><label class="compact">间隔 ms<input data-ui="interval" type="number" min="50" max="3600000" value="1000"></label><button data-ui="repeat" type="button">定时发送</button><button data-ui="send" type="button" class="primary">发送</button></div></div>`;
      this.$ = name => root.querySelector(`[data-ui="${name}"]`);
      for (const name of ['hex-display', 'timestamp']) this.$(name).onchange = () => this.render();
      this.$('clear').onclick = () => { this.records = []; this.size = 0; this.rx = 0; this.tx = 0; this.render(); };
      this.$('save').onclick = () => {
        this.render(); const url = URL.createObjectURL(new Blob([this.$('log').textContent], {type:'text/plain;charset=utf-8'})), a = document.createElement('a');
        a.href = url; a.download = 'sitedeck-' + new Date().toISOString().replace(/[:.]/g, '-') + '.log'; a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
      };
      this.$('send').onclick = () => this.send().catch(error => this.fail(error));
      this.$('repeat').onclick = () => this.timer !== null ? this.stopRepeat() : this.startRepeat();
      if (options.readOnly) this.disable('只读会话：可以查看数据，不能发送');
    }
    add(event) {
      if (this.disposed) return;
      const bytes = decode64(event.data), record = {...event, bytes};
      if (record.direction === 'rx') this.rx += bytes.length;
      if (record.direction === 'tx') this.tx += bytes.length;
      this.records.push(record); this.size += bytes.length + (event.message || '').length;
      while (this.size > 524288 || this.records.length > 2048) { const first = this.records.shift(); this.size -= first.bytes.length + (first.message || '').length; }
      if (this.renderTimer === null) this.renderTimer = setTimeout(() => { this.renderTimer = null; this.render(); }, 60);
    }
    note(message) { this.add({time:new Date().toISOString(), direction:'info', message}); }
    render() {
      if (this.disposed) return;
      const hex = this.$('hex-display').checked, timestamp = this.$('timestamp').checked;
      const decoders = new Map();
      this.$('log').textContent = this.records.map(record => {
        const date = new Date(record.time || Date.now());
        const stamp = timestamp ? '[' + date.toLocaleTimeString('zh-CN', {hour12:false}) + '.' + String(date.getMilliseconds()).padStart(3, '0') + '] ' : '';
        const key = record.direction + '/' + (record.peer || '');
        if (!decoders.has(key)) decoders.set(key, new TextDecoder());
        let text = record.message || (hex ? Array.from(record.bytes, b => b.toString(16).padStart(2, '0').toUpperCase()).join(' ') : decoders.get(key).decode(record.bytes, {stream:true}));
        // Logs are text nodes: bytes cannot inject HTML or terminal escape actions.
        if (!hex) text = text.replace(/\r\n/g, '\n').replace(/\r/g, '\\r').replace(/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/g, c => '\\x' + c.charCodeAt(0).toString(16).padStart(2, '0'));
        return stamp + record.direction.toUpperCase() + (record.peer ? ' [' + record.peer + ']' : '') + '  ' + text;
      }).join('\n');
      this.$('counts').textContent = 'RX ' + this.rx.toLocaleString() + ' B · TX ' + this.tx.toLocaleString() + ' B';
      if (this.$('autoscroll').checked) this.$('log').scrollTop = this.$('log').scrollHeight;
    }
    async send() {
      if (this.disposed || this.sending || this.options.readOnly || this.disabled) return;
      const endings = {'':'', crlf:'\r\n', lf:'\n', cr:'\r'};
      const bytes = parseSend(this.$('text').value, this.$('hex-send').checked, endings[this.$('ending').value]);
      this.sending = true; this.$('send').disabled = true;
      try { await this.options.send(encode64(bytes)); this.$('error').textContent = ''; }
      finally { this.sending = false; if (!this.disposed && !this.disabled) this.$('send').disabled = false; }
    }
    startRepeat() {
      const interval = Number(this.$('interval').value);
      if (!Number.isInteger(interval) || interval < 50 || interval > 3600000) { this.fail(new Error('定时间隔范围为 50–3600000 ms')); return; }
      this.$('repeat').textContent = '停止定时'; this.$('repeat').classList.add('repeating');
      const tick = async () => {
        try { await this.send(); }
        catch (error) { this.fail(error); return; }
        if (!this.disposed && this.timer !== null) this.timer = setTimeout(tick, interval);
      };
      this.timer = setTimeout(tick, 0);
    }
    stopRepeat() { if (this.timer !== null) clearTimeout(this.timer); this.timer = null; this.$('repeat').textContent = '定时发送'; this.$('repeat').classList.remove('repeating'); }
    fail(error) { this.stopRepeat(); this.$('error').textContent = error.message; }
    disable(reason) { this.stopRepeat(); this.disabled = true; this.$('send').disabled = true; this.$('repeat').disabled = true; this.$('text').disabled = true; if (reason) this.$('error').textContent = reason; }
    dispose() { this.stopRepeat(); clearTimeout(this.renderTimer); this.disposed = true; }
  }
  window.SiteDebug = {DataPanel, parseSend, encode64, decode64};
})();
