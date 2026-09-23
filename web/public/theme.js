// 主题引导：在首屏绘制前按本地存储应用主题，避免闪烁。
// 独立为外部脚本以满足 CSP `script-src 'self'`（内联脚本会被拦）。
(function () {
  try {
    var t = localStorage.getItem('ppts-theme');
    if (t !== 'dark' && t !== 'light') t = 'dark';
    document.documentElement.dataset.theme = t;
    var m = document.getElementById('theme-color');
    if (m) m.setAttribute('content', t === 'light' ? '#eef1f6' : '#10131a');
  } catch (e) {
    document.documentElement.dataset.theme = 'dark';
  }
})();
