if (!Array.prototype.forEach) {
  // Simplified iterator for browsers without forEach support
  Array.prototype.forEach = function(cb) {
    if (typeof this.length != 'number') return;
    if (typeof callback != 'function') return;

    for (var i = 0; i < this.length; i++) cb(this[i]);
  }
}

// Copies text into the system clipboard.
// Returns a promise that resolves to the success flag, never rejects.
function copyToClipboard(text) {
  // Async clipboard API is only available on secure origins, so pgweb served
  // over plain http on a remote host still has to fall back to execCommand.
  if (navigator.clipboard && window.isSecureContext) {
    return navigator.clipboard.writeText(text).then(
      function()  { return true; },
      function()  { return copyToClipboardFallback(text); }
    );
  }

  return Promise.resolve(copyToClipboardFallback(text));
}

function copyToClipboardFallback(text) {
  const element = document.createElement("textarea");
  element.style.display = "none;"
  element.value = text;

  document.body.appendChild(element);
  element.focus();
  element.setSelectionRange(0, element.value.length);

  const copied = document.execCommand("copy");
  document.body.removeChild(element);

  return copied;
}

function guid() {
  function s4() { return Math.floor((1 + Math.random()) * 0x10000).toString(16).substring(1); }
  return [s4(), s4(), "-", s4(), "-", s4(), "-", s4(), "-", s4(), s4(), s4()].join("");
}

