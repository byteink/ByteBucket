// The async Clipboard API only exists in a secure context (HTTPS, or
// localhost/127.0.0.1). When the dashboard is served over plain HTTP on a
// remote host or IP, navigator.clipboard is undefined and writeText throws
// "Cannot read properties of undefined (reading 'writeText')". Fall back to the
// legacy execCommand path so copy still works there instead of crashing.
export async function copyText(value: string): Promise<void> {
  const clip = navigator.clipboard;
  if (clip && typeof clip.writeText === 'function') {
    await clip.writeText(value);
    return;
  }

  const ta = document.createElement('textarea');
  ta.value = value;
  // Off-screen but selectable; must not be readonly or iOS Safari blocks copy.
  ta.style.position = 'fixed';
  ta.style.top = '0';
  ta.style.left = '0';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.focus();
  ta.select();
  try {
    if (!document.execCommand('copy')) {
      throw new Error('Copy is unavailable here; select the text and copy manually.');
    }
  } finally {
    ta.remove();
  }
}
