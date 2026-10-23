package browser

// outlineJS 는 현재 프레임의 폼·입력칸·버튼·링크·iframe·표 머리글을 한 줄씩 요약한다.
// 화면 구조를 파악해 자동화 코드를 쓰는 데 필요한 것(id, name, 라벨, 글자, 동작)만 담고,
// 입력된 값은 읽지 않는다.
const outlineJS = `() => {
  const MAX = 700;
  const out = [];
  const txt = (s, n) => (s || '').replace(/\s+/g, ' ').trim().slice(0, n);
  // 쿼리뿐 아니라 ;jsessionid=... 같은 경로 매개변수도 버린다.
  const path = (u) => { if (!u) return ''; try { return new URL(u, location.href).pathname.split(';')[0]; } catch (e) { return String(u).split(/[?#;]/)[0]; } };
  const visible = (el) => {
    const r = el.getBoundingClientRect(); const cs = getComputedStyle(el);
    return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none';
  };
  const labelOf = (el) => {
    if (el.id) { const l = document.querySelector('label[for="' + CSS.escape(el.id) + '"]'); if (l) return txt(l.innerText, 40); }
    const pl = el.closest('label'); if (pl) return txt(pl.innerText, 40);
    const al = el.getAttribute('aria-label'); if (al) return txt(al, 40);
    const td = el.closest('td');
    if (td) {
      const p = td.previousElementSibling; if (p) return txt(p.innerText, 40);
      const th = td.parentElement && td.parentElement.querySelector('th'); if (th) return txt(th.innerText, 40);
    }
    return '';
  };
  const kv = (el, names) => names.map(n => { const v = el.getAttribute(n); return v ? n + '=' + JSON.stringify(txt(v, 60)) : ''; }).filter(Boolean).join(' ');
  const flags = (el) => ['required', 'readonly', 'disabled', 'checked', 'multiple'].filter(f => el.hasAttribute(f)).join(',');
  const lab = (el) => { const l = labelOf(el); return l ? ' label=' + JSON.stringify(l) : ''; };
  const forms = Array.from(document.forms);
  out.push('title=' + JSON.stringify(txt(document.title, 80)) + ' url=' + path(location.href));
  document.querySelectorAll('h1, h2, h3').forEach((h, i) => { if (i < 20) out.push('[h] ' + txt(h.innerText, 60)); });
  const nodes = document.querySelectorAll('form, input, select, textarea, button, a[href], [role=button], iframe, frame, [onclick], table');
  for (const el of nodes) {
    if (out.length >= MAX) { out.push('… (생략: 항목이 너무 많음)'); break; }
    const tag = el.tagName.toLowerCase();
    const hid = visible(el) ? '' : ' (숨김)';
    const f = el.form ? ' form=' + (el.form.id || el.form.name || ('#' + forms.indexOf(el.form))) : '';
    const base = kv(el, ['id', 'name']);
    let line = '';
    if (tag === 'form') {
      line = '[form] ' + base + ' action=' + path(el.getAttribute('action')) + ' ' + kv(el, ['method', 'target']);
    } else if (tag === 'input') {
      const type = (el.getAttribute('type') || 'text').toLowerCase();
      const isBtn = ['button', 'submit', 'reset', 'image'].includes(type);
      line = '[input ' + type + '] ' + base + ' ' + kv(el, ['placeholder', 'maxlength', 'title']) + (isBtn ? ' ' + kv(el, ['value']) : '') +
        lab(el) + ' ' + flags(el) + f;
    } else if (tag === 'select') {
      const opts = Array.from(el.options).slice(0, 8).map(o => txt(o.text, 20));
      line = '[select] ' + base + lab(el) + ' options=' + el.options.length + ' first=' + JSON.stringify(opts) + ' ' + flags(el) + f;
    } else if (tag === 'textarea') {
      line = '[textarea] ' + base + ' ' + kv(el, ['placeholder', 'maxlength']) + lab(el) + ' ' + flags(el) + f;
    } else if (tag === 'button' || el.getAttribute('role') === 'button') {
      line = '[button] ' + base + ' ' + kv(el, ['type', 'onclick']) + ' text=' + JSON.stringify(txt(el.innerText, 40)) + f;
    } else if (tag === 'a') {
      line = '[a] ' + kv(el, ['id', 'onclick', 'target']) + ' text=' + JSON.stringify(txt(el.innerText, 40)) + ' href=' + path(el.getAttribute('href'));
    } else if (tag === 'iframe' || tag === 'frame') {
      line = '[' + tag + '] ' + base + ' src=' + path(el.getAttribute('src'));
    } else if (tag === 'table') {
      const th = Array.from(el.querySelectorAll('th')).slice(0, 10).map(t => txt(t.innerText, 20));
      if (!th.length) continue;
      line = '[table] ' + base + ' headers=' + JSON.stringify(th) + ' rows=' + el.rows.length;
    } else {
      line = '[' + tag + ' onclick] ' + kv(el, ['id', 'class', 'onclick']) + ' text=' + JSON.stringify(txt(el.innerText, 40));
    }
    out.push(line.replace(/\s+/g, ' ').trim() + hid);
  }
  return out;
}`
