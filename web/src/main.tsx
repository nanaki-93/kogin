import { StrictMode } from 'react';
import { setNonce } from 'get-nonce';
import { createRoot } from 'react-dom/client';
import '@fontsource-variable/inter';
import '@fontsource-variable/jetbrains-mono';
import './styles/tokens.css';
import './styles/base.css';
import './styles/components.css';
import './styles/pages.css';
import { App } from './App';

// Dialog scroll locking injects a <style> tag; the CSP admits it only with the
// per-response nonce the daemon writes into the document.
const nonce = document.querySelector<HTMLMetaElement>('meta[name="kasuri-style-nonce"]')?.content;
if (nonce && !nonce.startsWith('__')) setNonce(nonce);

const root = document.getElementById('root');
if (root) {
  createRoot(root).render(
    <StrictMode>
      <App />
    </StrictMode>,
  );
}
