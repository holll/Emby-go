let token = localStorage.getItem('emby_token');
const root = document.querySelector('#auth-root');
const esc = value => String(value ?? '').replace(/[&<>'"]/g, char => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;' }[char]));

async function status() {
  const response = await fetch('/api/auth/status');
  if (!response.ok) throw new Error('无法读取初始化状态');
  return response.json();
}

function render(mode, error = '', busy = false) {
  const initializing = mode === 'initialize';
  const copy = initializing
    ? '首次运行需要创建唯一管理员账户。账户信息仅保存在本机 SQLite 数据库中，不外传、不可恢复。'
    : '使用管理员账户进入媒体库控制台。浏览海报墙请使用 Yamby / iPlay 等 Emby 客户端。';
  root.innerHTML = `
    <section class="auth-card">
      <p class="eyebrow">Emby-go · Archive Console</p>
      <h1>${initializing ? '建立管理员账户' : '欢迎回来'}</h1>
      <p class="auth-copy">${copy}</p>
      <form id="auth-form" class="auth-form">
        <div class="field">
          <label for="auth-user">管理员账号</label>
          <input id="auth-user" name="Username" autocomplete="username" minlength="3" required ${busy ? 'disabled' : 'autofocus'} placeholder="至少 3 个字符">
        </div>
        <div class="field">
          <label for="auth-pw">密码</label>
          <input id="auth-pw" name="Pw" type="password" autocomplete="${initializing ? 'new-password' : 'current-password'}" minlength="9" required ${busy ? 'disabled' : ''} placeholder="至少 9 个字符">
        </div>
        ${initializing ? `
        <div class="field">
          <label for="auth-confirm">确认密码</label>
          <input id="auth-confirm" name="confirm" type="password" autocomplete="new-password" required ${busy ? 'disabled' : ''} placeholder="再次输入密码">
        </div>` : ''}
        <p class="auth-error" role="alert">${esc(error)}</p>
        <button class="btn btn-accent" type="submit" ${busy ? 'disabled' : ''}>${busy ? '请稍候…' : (initializing ? '完成初始化' : '登录管理后台')}</button>
      </form>
      <p class="auth-note">NFO 为元数据真源 · 仅 http/https .strm 进入 Emby</p>
    </section>`;

  const form = document.querySelector('#auth-form');
  form.addEventListener('submit', event => submit(event, initializing));
  if (!busy && initializing) document.querySelector('#auth-user')?.focus();
}

async function submit(event, initializing) {
  event.preventDefault();
  const data = Object.fromEntries(new FormData(event.target));
  if (initializing && data.Pw !== data.confirm) {
    render('initialize', '两次输入的密码不一致');
    return;
  }
  render(initializing ? 'initialize' : 'login', '', true);
  const path = initializing ? '/api/auth/initialize' : '/Users/AuthenticateByName';
  const payload = { Username: data.Username, Pw: data.Pw };
  try {
    const response = await fetch(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    });
    if (!response.ok) {
      let message = '请求失败';
      try { message = (await response.json()).error || message; } catch { /* ignore */ }
      render(initializing ? 'initialize' : 'login', message);
      return;
    }
    if (initializing) {
      render('login', '初始化完成，请使用新账户登录');
      return;
    }
    token = (await response.json()).AccessToken;
    localStorage.setItem('emby_token', token);
    window.location.replace('/admin');
  } catch (error) {
    render(initializing ? 'initialize' : 'login', error.message || '网络错误');
  }
}

(async () => {
  if (token) {
    try {
      const response = await fetch('/Users/Me', { headers: { 'X-Emby-Token': token } });
      if (response.ok) { window.location.replace('/admin'); return; }
    } catch { /* fall through to login */ }
    localStorage.removeItem('emby_token');
  }
  try {
    render((await status()).initialized ? 'login' : 'initialize');
  } catch (error) {
    render('login', error.message || '无法连接服务');
  }
})();
