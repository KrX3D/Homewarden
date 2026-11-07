import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import '@fontsource/ibm-plex-sans/400.css';
import '@fontsource/ibm-plex-sans/500.css';
import '@fontsource/ibm-plex-sans/600.css';
import '@fontsource/bricolage-grotesque/600.css';
import '@fontsource/bricolage-grotesque/700.css';
import '@fontsource/jetbrains-mono/400.css';
import './styles.css';
import './styles-devices.css';
import { SessionProvider } from './session';
import './features/credentials';
import './features/software';
import './features/backups';
import './features/automation';
import './features/notifications';
import './features/monitors';
import './features/resources';
import './features/sso';
import './features/network';
import App from './App';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BrowserRouter>
      <SessionProvider>
        <App />
      </SessionProvider>
    </BrowserRouter>
  </StrictMode>,
);
