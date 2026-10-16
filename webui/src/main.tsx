import { createRoot } from 'react-dom/client';
import { Provider } from './ui';
import { Documents } from './documents';
import { App } from './app';
import { bootstrap } from './bootstrap';
import './styles.css';

createRoot(document.getElementById('root')!).render(<Provider>{bootstrap.page === 'console' ? <App /> : <Documents />}</Provider>);
