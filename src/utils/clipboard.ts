// copyText copies text to the clipboard and reports whether it worked.
//
// navigator.clipboard only exists in secure contexts (HTTPS or localhost), so
// the GUI served over plain HTTP from another host falls back to
// document.execCommand('copy'). The hidden textarea used for that fallback is
// placed inside the open modal, if any: PatternFly modals trap focus, so a
// textarea appended to document.body cannot be selected while one is open.
export async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // fall through to the legacy path
    }
  }

  const active = document.activeElement;
  const container = (active instanceof HTMLElement && active.closest<HTMLElement>('[role="dialog"]')) || document.body;
  const textArea = document.createElement('textarea');
  textArea.value = text;
  textArea.setAttribute('readonly', '');
  textArea.style.position = 'fixed';
  textArea.style.top = '0';
  textArea.style.left = '0';
  textArea.style.opacity = '0';
  container.appendChild(textArea);
  textArea.focus();
  textArea.select();
  let copied = false;
  try {
    copied = document.execCommand('copy');
  } catch {
    copied = false;
  }
  container.removeChild(textArea);
  if (active instanceof HTMLElement) active.focus();
  return copied;
}
