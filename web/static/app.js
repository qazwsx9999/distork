const appContext = window.APP_CONTEXT || {};
const state = {
  user: appContext.user || { email: '', displayName: '' },
  servers: Array.isArray(appContext.servers) ? appContext.servers.map((server) => ({
    ...server,
    unread: new Map(),
  })) : [],
  membersByServer: new Map(),
  messagesByChannel: new Map(),
  messageIds: new Set(),
  activeServerId: appContext.activeServerId || null,
  activeChannelId: appContext.activeChannelId || null,
  routes: appContext.routes || {},
  loading: {
    members: false,
    messages: false,
  },
  eventSource: null,
};

const refs = {
  root: null,
  serverList: null,
  channelList: null,
  memberList: null,
  messageList: null,
  messageWrapper: null,
  composerInput: null,
  status: null,
  headerTitle: null,
  channelBreadcrumb: null,
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
  if (!source) return '?';
  const parts = source.split(/\s+/).filter(Boolean);
  if (parts.length === 0) return source.slice(0, 2).toUpperCase();
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase();
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
}

function dayKey(timestamp) {
  const date = new Date(timestamp);
  if (Number.isNaN(date.getTime())) return '';
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`;
}

function ensureArray(value) {
  return Array.isArray(value) ? value : [];
}

function ensureServerMap() {
  state.servers.forEach((server) => {
    if (!(server.channels && Array.isArray(server.channels))) {
      server.channels = [];
    }
    if (!(server.unread instanceof Map)) {
      server.unread = new Map();
    }
  });
}

function initStateFromContext() {
  ensureServerMap();

  const initialMembers = ensureArray(appContext.members);
  if (state.activeServerId) {
    state.membersByServer.set(state.activeServerId, initialMembers);
  }

  const initialMessages = ensureArray(appContext.messages);
  if (state.activeChannelId) {
    const bucket = [];
    initialMessages.forEach((msg) => {
      bucket.push(msg);
      state.messageIds.add(`${msg.channelId}:${msg.id}`);
    });
    state.messagesByChannel.set(state.activeChannelId, bucket);
  }
}

function setStatus(message, tone = '') {
  if (!refs.status) return;
  refs.status.textContent = message || '';
  refs.status.dataset.tone = tone || '';
}

function isNearBottom(element) {
  if (!element) return true;
  const threshold = 120;
  return element.scrollTop + element.clientHeight >= element.scrollHeight - threshold;
}

function scrollToBottom(force = false) {
  if (!refs.messageList) return;
  if (force || isNearBottom(refs.messageList)) {
    refs.messageList.scrollTop = refs.messageList.scrollHeight;
  }
}

function findServer(serverId) {
  return state.servers.find((srv) => srv.id === serverId);
}

function ensureChannelBuffer(channelId) {
  if (!state.messagesByChannel.has(channelId)) {
    state.messagesByChannel.set(channelId, []);
  }
  return state.messagesByChannel.get(channelId);
}

async function fetchJSON(url, options = {}) {
  const response = await fetch(url, {
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    ...options,
  });
  if (!response.ok) {
    const error = new Error(`Request failed: ${response.status}`);
    error.status = response.status;
    throw error;
  }
  return response.json();
}

function clearUnread(channelId, serverId) {
  const server = findServer(serverId);
  if (!server) return;
  if (server.unread.has(channelId)) {
    server.unread.delete(channelId);
    renderChannels();
  }
}

function addUnread(channelId, serverId) {
  if (channelId === state.activeChannelId) return;
  const server = findServer(serverId);
  if (!server) return;
  const current = server.unread.get(channelId) || 0;
  server.unread.set(channelId, current + 1);
  renderChannels();
}

function createServerNav() {
  const nav = document.createElement('nav');
  nav.className = 'server-bar';
  refs.serverList = document.createElement('ul');
  refs.serverList.className = 'server-list';
  nav.appendChild(refs.serverList);
  return nav;
}

function createChannelPanel() {
  const aside = document.createElement('aside');
  aside.className = 'channel-panel';

  const header = document.createElement('header');
  header.className = 'panel-header';

  refs.headerTitle = document.createElement('h2');
  refs.headerTitle.className = 'panel-title';
  header.appendChild(refs.headerTitle);

  aside.appendChild(header);

  refs.channelList = document.createElement('ul');
  refs.channelList.className = 'channel-list';
  aside.appendChild(refs.channelList);
  return aside;
}

function createChatPanel() {
  const main = document.createElement('main');
  main.className = 'chat-panel';

  const header = document.createElement('header');
  header.className = 'chat-header';

  refs.channelBreadcrumb = document.createElement('div');
  refs.channelBreadcrumb.className = 'chat-breadcrumb';
  header.appendChild(refs.channelBreadcrumb);

  const userContainer = document.createElement('div');
  userContainer.className = 'chat-user';
  userContainer.innerHTML = `
    <div class="chat-user-avatar">${initialsFrom(state.user.displayName, state.user.email)}</div>
    <div class="chat-user-meta">
      <span class="chat-user-name">${state.user.displayName || state.user.email}</span>
      <form method="post" action="/logout">
        <button type="submit" class="logout-btn">Log out</button>
      </form>
    </div>
  `;
  header.appendChild(userContainer);

  main.appendChild(header);

  refs.messageWrapper = document.createElement('section');
  refs.messageWrapper.className = 'message-wrapper';

  refs.messageList = document.createElement('div');
  refs.messageList.className = 'message-list';
  refs.messageList.dataset.lastDay = '';

  refs.messageWrapper.appendChild(refs.messageList);
  main.appendChild(refs.messageWrapper);

  refs.status = document.createElement('div');
  refs.status.className = 'status';
  refs.status.setAttribute('role', 'status');
  refs.status.setAttribute('aria-live', 'polite');
  main.appendChild(refs.status);

  const composer = document.createElement('form');
  composer.className = 'composer';

  const textarea = document.createElement('textarea');
  textarea.placeholder = 'Message #general';
  textarea.rows = 1;
  textarea.required = true;
  textarea.autocomplete = 'off';
  textarea.spellcheck = true;

  const button = document.createElement('button');
  button.type = 'submit';
  button.className = 'composer-send';
  button.textContent = 'Send';

  composer.appendChild(textarea);
  composer.appendChild(button);

  textarea.addEventListener('input', () => {
    textarea.style.height = 'auto';
    textarea.style.height = `${Math.min(textarea.scrollHeight, 200)}px`;
  });

  textarea.addEventListener('keydown', (event) => {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      composer.requestSubmit();
    }
  });

  composer.addEventListener('submit', handleSubmit);

  refs.composerInput = textarea;
  main.appendChild(composer);
  return main;
}

function createMemberPanel() {
  const aside = document.createElement('aside');
  aside.className = 'member-panel';

  const header = document.createElement('header');
  header.className = 'panel-header';
  const title = document.createElement('h2');
  title.className = 'panel-title';
  title.textContent = 'Members';
  header.appendChild(title);
  aside.appendChild(header);

  refs.memberList = document.createElement('ul');
  refs.memberList.className = 'member-list';
  aside.appendChild(refs.memberList);
  return aside;
}

function renderServers() {
  if (!refs.serverList) return;
  refs.serverList.innerHTML = '';

  state.servers.forEach((server) => {
    const item = document.createElement('li');
    item.className = 'server-item';
    if (server.id === state.activeServerId) {
      item.classList.add('is-active');
    }

    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'server-button';
    button.textContent = initialsFrom(server.name, server.slug);
    button.title = server.name;
    button.addEventListener('click', () => switchServer(server.id));

    item.appendChild(button);
    refs.serverList.appendChild(item);
  });
}

function renderChannels() {
  if (!refs.channelList) return;
  refs.channelList.innerHTML = '';

  const server = findServer(state.activeServerId);
  if (!server) return;

  if (refs.headerTitle) {
    refs.headerTitle.textContent = server.name;
  }

  server.channels.forEach((channel) => {
    const item = document.createElement('li');
    item.className = 'channel-item';
    if (channel.id === state.activeChannelId) {
      item.classList.add('is-active');
    }

    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'channel-button';
    button.innerHTML = `<span class="hash">#</span><span>${channel.name}</span>`;
    button.addEventListener('click', () => switchChannel(channel.id));

    const unreadCount = server.unread.get(channel.id) || 0;
    if (unreadCount > 0) {
      const badge = document.createElement('span');
      badge.className = 'channel-unread';
      badge.textContent = unreadCount > 9 ? '9+' : unreadCount.toString();
      button.appendChild(badge);
    }

    item.appendChild(button);
    refs.channelList.appendChild(item);
  });
}

function renderMembers() {
  if (!refs.memberList) return;
  refs.memberList.innerHTML = '';

  const members = state.membersByServer.get(state.activeServerId) || [];
  members.forEach((member) => {
    const item = document.createElement('li');
    item.className = 'member-item';
    item.innerHTML = `
      <div class="member-avatar">${initialsFrom(member.displayName, member.email)}</div>
      <div class="member-meta">
        <span class="member-name">${member.displayName || member.email}</span>
        <span class="member-role">${member.role}</span>
      </div>
    `;
    refs.memberList.appendChild(item);
  });
}

function appendDayDivider(day) {
  if (!refs.messageList) return;
  const divider = document.createElement('div');
  divider.className = 'message-divider';
  const parsed = new Date(day);
  divider.textContent = Number.isNaN(parsed.getTime()) ? day : dayFormatter.format(parsed);
  refs.messageList.appendChild(divider);
  refs.messageList.dataset.lastDay = day;
}

function createMessageElement(msg) {
  const wrapper = document.createElement('article');
  wrapper.className = 'message';
  if ((msg.authorEmail || '').toLowerCase() === (state.user.email || '').toLowerCase()) {
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

  const timeNode = document.createElement('time');
  timeNode.className = 'message-time';
  const created = new Date(msg.createdAt);
  if (!Number.isNaN(created.getTime())) {
    timeNode.dateTime = created.toISOString();
    timeNode.textContent = timeFormatter.format(created);
  }
  header.appendChild(timeNode);

  body.appendChild(header);

  const content = document.createElement('p');
  content.className = 'message-content';
  const safe = (msg.content || '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
    .replace(/\n/g, '<br />');
  content.innerHTML = safe;
  body.appendChild(content);

  wrapper.appendChild(body);
  return wrapper;
}

function renderMessages() {
  if (!refs.messageList) return;
  refs.messageList.innerHTML = '';
  refs.messageList.dataset.lastDay = '';

  const messages = state.messagesByChannel.get(state.activeChannelId) || [];
  let lastDay = '';

  messages.forEach((msg) => {
    const key = dayKey(msg.createdAt);
    if (key && key !== lastDay) {
      appendDayDivider(key);
      lastDay = key;
    }
    refs.messageList.appendChild(createMessageElement(msg));
  });

  scrollToBottom(true);
}

async function switchServer(serverId) {
  if (state.activeServerId === serverId) return;
  state.activeServerId = serverId;
  const server = findServer(serverId);
  if (!server) return;

  const firstChannel = server.channels && server.channels[0];
  state.activeChannelId = firstChannel ? firstChannel.id : null;

  renderServers();
  renderChannels();
  updateBreadcrumb();

  await Promise.all([
    ensureMembersLoaded(serverId),
    ensureMessagesLoaded(state.activeChannelId),
  ]);

  clearUnread(state.activeChannelId, state.activeServerId);
  renderMembers();
  renderMessages();
  updateComposerPlaceholder();
  setStatus('');
}

async function switchChannel(channelId) {
  if (!channelId || state.activeChannelId === channelId) return;
  const server = findServer(state.activeServerId);
  if (!server) return;
  const channel = server.channels.find((ch) => ch.id === channelId);
  if (!channel) return;

  state.activeChannelId = channelId;
  renderChannels();
  updateBreadcrumb();
  await ensureMessagesLoaded(channelId, { force: false });
  clearUnread(channelId, state.activeServerId);
  renderMessages();
  updateComposerPlaceholder();
  setStatus('');
}

function updateComposerPlaceholder() {
  if (!refs.composerInput) return;
  const server = findServer(state.activeServerId);
  const channel = server ? server.channels.find((ch) => ch.id === state.activeChannelId) : null;
  const name = channel ? channel.name : 'channel';
  refs.composerInput.placeholder = `Message #${name}`;
}

function updateBreadcrumb() {
  if (!refs.channelBreadcrumb) return;
  const server = findServer(state.activeServerId);
  const channel = server ? server.channels.find((ch) => ch.id === state.activeChannelId) : null;
  refs.channelBreadcrumb.textContent = server && channel ? `${server.name} / #${channel.name}` : '';
}

async function ensureMembersLoaded(serverId) {
  if (!serverId) return;
  if (state.membersByServer.has(serverId)) return;
  state.loading.members = true;
  try {
    const members = await fetchJSON(`${state.routes.servers}/${serverId}/members`);
    state.membersByServer.set(serverId, members);
  } catch (error) {
    console.error('load members', error);
    setStatus('Could not load members.', 'error');
    state.membersByServer.set(serverId, []);
  } finally {
    state.loading.members = false;
  }
}

async function ensureMessagesLoaded(channelId, { force = false } = {}) {
  if (!channelId) return;
  if (!force && state.messagesByChannel.has(channelId) && state.messagesByChannel.get(channelId).length > 0) {
    return;
  }
  state.loading.messages = true;
  try {
    const messages = await fetchJSON(`${state.routes.channels}/${channelId}/messages?limit=200`);
    const bucket = [];
    messages.forEach((msg) => {
      const key = `${msg.channelId}:${msg.id}`;
      if (!state.messageIds.has(key)) {
        state.messageIds.add(key);
        bucket.push(msg);
      }
    });
    state.messagesByChannel.set(channelId, bucket);
  } catch (error) {
    console.error('load messages', error);
    setStatus('Could not load messages.', 'error');
  } finally {
    state.loading.messages = false;
  }
}

async function handleSubmit(event) {
  event.preventDefault();
  if (!refs.composerInput || state.loading.messages) return;
  const content = refs.composerInput.value.trim();
  if (!content) return;

  setStatus('Sending…', 'pending');

  try {
    const payload = await fetchJSON(`${state.routes.channels}/${state.activeChannelId}/messages`, {
      method: 'POST',
      body: JSON.stringify({ content }),
    });

    pushMessage(payload, { scroll: true });
    refs.composerInput.value = '';
    refs.composerInput.style.height = 'auto';
    setStatus('');
  } catch (error) {
    console.error('send message', error);
    setStatus('Failed to send message.', 'error');
  }
}

function pushMessage(msg, { scroll = false } = {}) {
  const key = `${msg.channelId}:${msg.id}`;
  if (state.messageIds.has(key)) return;
  state.messageIds.add(key);

  const bucket = ensureChannelBuffer(msg.channelId);
  bucket.push(msg);
  bucket.sort((a, b) => new Date(a.createdAt) - new Date(b.createdAt));

  if (msg.channelId === state.activeChannelId) {
    renderMessages();
    if (scroll) scrollToBottom(true);
  } else {
    const channel = findServer(state.activeServerId)?.channels.find((ch) => ch.id === msg.channelId);
    addUnread(msg.channelId, channel ? channel.serverId : state.activeServerId);
  }
}

function connectEvents() {
  if (!state.routes.events) return;
  if (state.eventSource) {
    state.eventSource.close();
  }

  setStatus('Connecting…', 'pending');

  const es = new EventSource(state.routes.events, { withCredentials: true });
  state.eventSource = es;

  es.addEventListener('open', () => {
    setStatus('');
  });

  es.addEventListener('message', (event) => {
    try {
      const payload = JSON.parse(event.data);
      pushMessage(payload);
    } catch (error) {
      console.error('parse event payload', error);
    }
  });

  es.addEventListener('error', () => {
    setStatus('Connection lost. Reconnecting…', 'error');
    es.close();
    setTimeout(connectEvents, 3000);
  });
}

async function bootstrapLatest() {
  try {
    const payload = await fetchJSON(state.routes.bootstrap);
    state.servers = payload.servers.map((server) => ({
      ...server,
      unread: new Map(),
    }));
    state.activeServerId = payload.activeServerId;
    state.activeChannelId = payload.activeChannelId;
    state.membersByServer = new Map([[payload.activeServerId, payload.members || []]]);
    state.messagesByChannel = new Map();
    state.messageIds = new Set();
    ensureServerMap();

    (payload.messages || []).forEach((msg) => {
      const key = `${msg.channelId}:${msg.id}`;
      state.messageIds.add(key);
      ensureChannelBuffer(msg.channelId).push(msg);
    });
    renderServers();
    renderChannels();
    renderMembers();
    renderMessages();
    updateBreadcrumb();
    updateComposerPlaceholder();
  } catch (error) {
    console.error('bootstrap refresh', error);
  }
}

function renderApp() {
  const root = document.getElementById('app');
  root.innerHTML = '';
  refs.root = root;

  const shell = document.createElement('div');
  shell.className = 'app-shell';

  shell.appendChild(createServerNav());
  shell.appendChild(createChannelPanel());
  shell.appendChild(createChatPanel());
  shell.appendChild(createMemberPanel());

  root.appendChild(shell);
}

async function init() {
  initStateFromContext();
  renderApp();
  renderServers();
  renderChannels();
  renderMembers();
  renderMessages();
  updateBreadcrumb();
  updateComposerPlaceholder();
  connectEvents();
  setStatus('');

  // Refresh bootstrap data after load to catch any updates.
  setTimeout(() => {
    bootstrapLatest();
  }, 2000);
}

window.addEventListener('beforeunload', () => {
  if (state.eventSource) {
    state.eventSource.close();
  }
});

window.addEventListener('DOMContentLoaded', init);
