import React from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import App from './App';
import { purgeLegacySession } from './lib/session';
import './styles.css';

// Older builds kept the admin secret in localStorage; drop it on every load so
// upgraded browsers do not keep carrying it.
purgeLegacySession();

const root = document.getElementById('root');
if (!root) {
  throw new Error('root element not found');
}

createRoot(root).render(
  <React.StrictMode>
    <BrowserRouter>
      <App />
    </BrowserRouter>
  </React.StrictMode>
);
