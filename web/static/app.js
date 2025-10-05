const appContext = window.APP_CONTEXT || {};
const state = {
  messages: [],
  messageIds: new Set(),
  sending: false,
};
const refs = {
  messageList: null,
  input: null,
  status: null,
};

const timeFormatter = new Intl.DateTimeFormat(undefined, {
  hour: '2-digit',
  minute: '2-digit',
});

const dayFormatter = new Intl.DateTimeFormat(undefined, {
  weekday: 'short',
  month: 'short',
  day: 'numeric',
});

function initialsFrom(name, fallback) {
  const source = (name || fallback || '').trim();
  if (!source) {
    return '?';
  }
  const parts = source.split(/\s+/).filter(Boolean);
  if (parts.length === 0) {
    return source.slice(0, 2).toUpperCase();
  }
  if (parts.length === 1) {
    return parts[0].slice(0, 2).toUpperCase();
  }
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
}

function escapeHTML(str) {
  return str
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function dayKey(timestamp) {
  const date = new Date(timestamp);
  if (Number.isNaN(date.getTime())) {
    return '';
  }
  const year = date.getFullYear();
  const month = `${date.getMonth() + 1}`.padStart(2, '0');
  const day = `${date.getDate()}`.padStart(2, '0');
  return `${year}-${month}-${day}`;
}

function isNearBottom(element) {
  if (!element) {
    return true;
  }
  const threshold = 120;
  return element.scrollTop + element.clientHeight >= element.scrollHeight - threshold;
}

function scrollToBottom(force = false) {
  if (!refs.messageList) {
    return;
  }
  if (force || isNearBottom(refs.messageList)) {
    refs.messageList.scrollTop = refs.messageList.scrollHeight;
  }
}

function setStatus(message, tone = '') {
  if (!refs.status) {
    return;
  }
  refs.status.textContent = message || '';
  refs.status.dataset.tone = tone || '';
}

function createMessageElement(msg) {
  const wrapper = document.createElement('article');
  wrapper.className = 'message';
  const mine = (msg.authorEmail || '').toLowerCase() === (appContext.user || '').toLowerCase();
  if (mine) {
    wrapper.classList.add('message--self');
  }

  const avatar = document.createElement('div');
  avatar.className = 'message-avatar';
  avatar.textContent = initialsFrom(msg.authorDisplayName, msg.authorEmail);
  wrapper.appendChild(avatar);

  const body = document.createElement('div');
  body.className = 'message-body';

  const header = document.createElement('header');
  header.className = 'message-meta';

  const author = document.createElement('span');
  author.className = 'message-author';
  author.textContent = msg.authorDisplayName || msg.authorEmail;
  header.appendChild(author);

  const timestamp = document.createElement('time');
  timestamp.className = 'message-time';
  const date = new Date(msg.createdAt);
  if (!Number.isNaN(date.getTime())) {
    timestamp.dateTime = date.toISOString();
    timestamp.textContent = timeFormatter.format(date);
  }
  header.appendChild(timestamp);

  body.appendChild(header);

  const content = document.createElement('p');
  content.className = 'message-content';
  const safe = escapeHTML(msg.content || '');
  content.innerHTML = safe.replace(/\n/g, '<br />');
  body.appendChild(content);

  wrapper.appendChild(body);
  return wrapper;
}

function appendDayDivider(dateKey) {
  if (!refs.messageList) {
    return;
  }
  const date = new Date(dateKey);
  const divider = document.createElement('div');
  divider.className = 'message-divider';
  if (!Number.isNaN(date.getTime())) {
    divider.textContent = dayFormatter.format(date);
  } else {
    divider.textContent = dateKey;
  }
  refs.messageList.appendChild(divider);
  refs.messageList.dataset.lastDay = dateKey;
}

function addMessage(msg, { isInitial = false } = {}) {
  if (!msg || typeof msg.id === 'undefined') {
    return;
  }
  if (state.messageIds.has(msg.id)) {
    return;
  }
  state.messageIds.add(msg.id);
  state.messages.push(msg);

  const shouldStick = isNearBottom(refs.messageList);
  const key = dayKey(msg.createdAt);
  if (key && refs.messageList.dataset.lastDay !== key) {
    appendDayDivider(key);
  }

  const node = createMessageElement(msg);
  refs.messageList.appendChild(node);

  if (!isInitial) {
    if (shouldStick) {
      scrollToBottom(true);
    }
  }
}

function renderHeader() {
  const header = document.createElement('header');
  header.className = 'topbar';

  const brand = document.createElement('div');
  brand.className = 'brand';
  const title = document.createElement('span');
  title.className = 'brand-name';
  title.textContent = 'EchoSphere';
  const subtitle = document.createElement('span');
  subtitle.className = 'brand-subtitle';
  subtitle.textContent = 'Private Room';
  brand.appendChild(title);
  brand.appendChild(subtitle);

  const userActions = document.createElement('div');
  userActions.className = 'user-actions';

  const current = document.createElement('div');
  current.className = 'current-user';
  const userAvatar = document.createElement('div');
  userAvatar.className = 'user-avatar';
  userAvatar.textContent = initialsFrom(appContext.displayName, appContext.user);
  const userName = document.createElement('span');
  userName.className = 'user-name';
  userName.textContent = appContext.displayName || appContext.user || 'You';
  current.appendChild(userAvatar);
  current.appendChild(userName);

  const logoutForm = document.createElement('form');
  logoutForm.method = 'post';
  logoutForm.action = '/logout';
  const logoutButton = document.createElement('button');
  logoutButton.type = 'submit';
  logoutButton.className = 'logout-btn';
  logoutButton.textContent = 'Log out';
  logoutForm.appendChild(logoutButton);

  userActions.appendChild(current);
  userActions.appendChild(logoutForm);

  header.appendChild(brand);
  header.appendChild(userActions);
  return header;
}

function buildComposer() {
  const form = document.createElement('form');
  form.className = 'composer';

  const textarea = document.createElement('textarea');
  textarea.placeholder = 'Write a message…';
  textarea.rows = 1;
  textarea.required = true;
  textarea.autocomplete = 'off';
  textarea.spellcheck = true;

  const button = document.createElement('button');
  button.type = 'submit';
  button.className = 'composer-send';
  button.textContent = 'Send';

  form.appendChild(textarea);
  form.appendChild(button);

  textarea.addEventListener('input', () => {
    textarea.style.height = 'auto';
    textarea.style.height = `${Math.min(textarea.scrollHeight, 200)}px`;
  });

  textarea.addEventListener('keydown', (event) => {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      form.requestSubmit();
    }
  });

  form.addEventListener('submit', handleSubmit);

  refs.input = textarea;
  return form;
}

async function handleSubmit(event) {
  event.preventDefault();
  if (state.sending) {
    return;
  }

  const value = refs.input.value.trim();
  if (!value) {
    return;
  }

  state.sending = true;
  setStatus('Sending…', 'pending');

  try {
    const response = await fetch(appContext.messagesUrl || '/api/messages', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({ content: value }),
      credentials: 'same-origin',
    });

    if (!response.ok) {
      throw new Error(`Request failed: ${response.status}`);
    }

    const payload = await response.json();
    addMessage(payload);
    refs.input.value = '';
    refs.input.style.height = 'auto';
    scrollToBottom(true);
    setStatus('');
  } catch (error) {
    console.error('send message', error);
    setStatus('Failed to send message. Try again.', 'error');
  } finally {
    state.sending = false;
  }
}

let eventSource;
let reconnectDelay = 2000;

function connectEvents() {
  if (!appContext.eventsUrl) {
    return;
  }
  if (eventSource) {
    eventSource.close();
  }

  setStatus('Connecting…', 'pending');

  eventSource = new EventSource(appContext.eventsUrl, { withCredentials: true });

  eventSource.addEventListener('open', () => {
    setStatus('');
    reconnectDelay = 2000;
  });

  eventSource.addEventListener('message', (event) => {
    try {
      const payload = JSON.parse(event.data);
      addMessage(payload);
    } catch (error) {
      console.error('parse event payload', error);
    }
  });

  eventSource.addEventListener('error', () => {
    setStatus('Connection lost. Reconnecting…', 'error');
    eventSource.close();
    const delay = reconnectDelay;
    reconnectDelay = Math.min(reconnectDelay * 2, 30000);
    setTimeout(connectEvents, delay);
  });
}

function renderApp() {
  const root = document.getElementById('app');
  root.innerHTML = '';

  const shell = document.createElement('div');
  shell.className = 'app-shell';
  root.appendChild(shell);

  shell.appendChild(renderHeader());

  const main = document.createElement('main');
  main.className = 'chat-main';
  shell.appendChild(main);

  const list = document.createElement('section');
  list.className = 'message-list';
  list.dataset.lastDay = '';
  main.appendChild(list);
  refs.messageList = list;

  const status = document.createElement('div');
  status.className = 'status';
  status.setAttribute('role', 'status');
  status.setAttribute('aria-live', 'polite');
  main.appendChild(status);
  refs.status = status;

  const composer = buildComposer();
  main.appendChild(composer);
}

function hydrateInitialMessages() {
  const initial = Array.isArray(appContext.messages) ? appContext.messages.slice() : [];
  initial.sort((a, b) => new Date(a.createdAt) - new Date(b.createdAt));
  initial.forEach((msg) => addMessage(msg, { isInitial: true }));
  scrollToBottom(true);
}

function init() {
  renderApp();
  hydrateInitialMessages();
  connectEvents();
}

window.addEventListener('beforeunload', () => {
  if (eventSource) {
    eventSource.close();
  }
});

window.addEventListener('DOMContentLoaded', init);
