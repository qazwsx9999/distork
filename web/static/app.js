const appContext = window.APP_CONTEXT || {};
const currentUserDisplay = appContext.displayName || appContext.user || 'Guest';

const servers = [
  { id: 'echo', name: 'Echo Labs', initials: 'EL', active: true },
  { id: 'design', name: 'DesignOps', initials: 'DO' },
  { id: 'marketing', name: 'Marketing', initials: 'MK' },
  { id: 'beta', name: 'Beta Crew', initials: 'BC' },
];

const channels = [
  { id: 'townhall', name: 'Town Hall', type: 'voice', live: true, mentions: 3 },
  { id: 'product-sync', name: 'Product Sync', type: 'voice', live: true },
  { id: 'co-working', name: 'Co-working', type: 'voice' },
  { id: 'flow-lounge', name: 'Flow Lounge', type: 'voice' },
  { id: 'fireside', name: 'Fireside Q&A', type: 'stage' },
];

const activeChannelId = 'townhall';

const voiceRooms = [
  {
    id: 'innovation-hub',
    name: 'Innovation Hub',
    status: 'Live',
    topic: 'Prototype feedback loop',
    participants: [
      { id: 'zoe', name: 'Zoe', initials: 'Z', speaking: true },
      { id: 'aaron', name: 'Aaron', initials: 'A' },
      { id: 'mia', name: 'Mia', initials: 'M' },
      { id: 'liam', name: 'Liam', initials: 'L' },
    ],
    listeners: 48,
    isJoined: true,
  },
  {
    id: 'design-review',
    name: 'Design Review',
    status: 'Focus',
    topic: 'UI polish sprint',
    participants: [
      { id: 'nora', name: 'Nora', initials: 'N' },
      { id: 'kim', name: 'Kim', initials: 'K' },
      { id: 'ari', name: 'Ari', initials: 'A' },
    ],
    listeners: 21,
  },
  {
    id: 'lab-42',
    name: 'Lab 42',
    status: 'Sync',
    topic: 'Infra weekly check-in',
    participants: [
      { id: 'dex', name: 'Dex', initials: 'D' },
      { id: 'kai', name: 'Kai', initials: 'K' },
    ],
    listeners: 9,
  },
];

const activityFeed = [
  {
    time: '2m',
    title: 'Zoe started sharing "Concept Deck v4"',
    detail: 'Design Review room',
    marker: 'ZS',
  },
  {
    time: '12m',
    title: 'Aaron joined Innovation Hub',
    detail: 'Invited by Mia',
    marker: 'A',
  },
  {
    time: '32m',
    title: 'Flow Lounge scheduled for 2 PM',
    detail: 'Daily co-working hangout',
    marker: 'FL',
  },
];

const screenShare = {
  host: 'Zoe Summers',
  topic: 'Concept Deck v4 – Sprint Narrative',
  viewers: ['Mia', 'Dex', 'Nora', 'Ishan'],
  isLive: true,
};

const controlState = {
  micMuted: false,
  deafened: false,
  screenSharing: true,
};

function createServerSidebar() {
  const sidebar = document.createElement('aside');
  sidebar.className = 'sidebar servers';

  const home = document.createElement('div');
  home.className = 'server-icon active';
  home.textContent = '⌂';
  sidebar.appendChild(home);

  servers.forEach((server) => {
    const icon = document.createElement('div');
    icon.className = `server-icon${server.active ? ' active' : ''}`;
    icon.textContent = server.initials;
    icon.title = server.name;
    sidebar.appendChild(icon);
  });

  const addServer = document.createElement('div');
  addServer.className = 'server-icon';
  addServer.textContent = '+';
  addServer.title = 'Create Server';
  sidebar.appendChild(addServer);

  return sidebar;
}

function createChannelsPanel() {
  const panel = document.createElement('section');
  panel.className = 'channels-panel';

  const title = document.createElement('div');
  title.className = 'section-title';
  title.textContent = 'Voice Rooms';
  panel.appendChild(title);

  const group = document.createElement('div');
  group.className = 'channel-group';
  panel.appendChild(group);

  channels.forEach((channel) => {
    const channelEl = document.createElement('div');
    channelEl.className = `channel${channel.id === activeChannelId ? ' active' : ''}`;

    const prefix = channel.type === 'voice' ? '🔊' : '🎙️';
    channelEl.innerHTML = `<span>${prefix}</span><span>${channel.name}</span>`;

    if (channel.mentions) {
      const badge = document.createElement('span');
      badge.className = 'badge';
      badge.textContent = channel.mentions;
      channelEl.appendChild(badge);
    }

    group.appendChild(channelEl);
  });

  const emptyState = document.createElement('div');
  emptyState.className = 'empty-state';
  emptyState.innerHTML = `
    <strong>Need a new space?</strong><br />
    Spin up a stage for workshops or a room for deep work.
  `;
  panel.appendChild(emptyState);

  const cta = document.createElement('button');
  cta.className = 'button primary';
  cta.textContent = 'Create Voice Room';
  cta.addEventListener('click', () => openModal('create-room'));
  panel.appendChild(cta);

  return panel;
}

function createHeader() {
  const header = document.createElement('header');
  header.className = 'main-header';

  const title = document.createElement('div');
  title.className = 'header-title';
  title.innerHTML = `
    <h1>Innovation Hub</h1>
    <span class="tag-pill">Live Focus</span>
  `;
  header.appendChild(title);

  const actions = document.createElement('div');
  actions.className = 'header-actions';

  const inviteButton = document.createElement('button');
  inviteButton.className = 'button muted';
  inviteButton.textContent = 'Invite';
  inviteButton.addEventListener('click', () => openModal('invite'));

  const startStageButton = document.createElement('button');
  startStageButton.className = 'button primary';
  startStageButton.textContent = 'Start Stage';
  startStageButton.addEventListener('click', () => openModal('stage'));

  actions.append(inviteButton, startStageButton);
  header.appendChild(actions);

  return header;
}

function createVoiceCards() {
  const grid = document.createElement('div');
  grid.className = 'voice-grid';

  voiceRooms.forEach((room) => {
    const card = document.createElement('article');
    card.className = `voice-card${room.isJoined ? ' active' : ''}`;

    const title = document.createElement('h3');
    title.innerHTML = `${room.name} <span class="status-pill">${room.status}</span>`;

    const topic = document.createElement('p');
    topic.className = 'voice-topic';
    topic.textContent = room.topic;

    const participants = document.createElement('div');
    participants.className = 'voice-participants';

    room.participants.forEach((participant) => {
      const avatar = document.createElement('div');
      avatar.className = 'avatar';
      avatar.textContent = participant.initials;
      if (participant.speaking) {
        avatar.style.boxShadow = '0 0 0 2px rgba(88, 101, 242, 0.6)';
        avatar.title = `${participant.name} — speaking`;
      } else {
        avatar.title = participant.name;
      }
      participants.appendChild(avatar);
    });

    const meta = document.createElement('div');
    meta.className = 'voice-meta';
    meta.innerHTML = `👂 ${room.listeners} listening`;

    const actions = document.createElement('div');
    actions.className = 'card-actions';

    const joinBtn = document.createElement('button');
    joinBtn.className = `button ${room.isJoined ? 'muted' : 'primary'}`;
    joinBtn.textContent = room.isJoined ? 'Leave' : 'Join';
    joinBtn.addEventListener('click', () => toggleJoinRoom(room.id));

    const shareBtn = document.createElement('button');
    shareBtn.className = 'button muted';
    shareBtn.textContent = 'Share Screen';
    shareBtn.addEventListener('click', () => openModal('share', room.name));

    actions.append(joinBtn, shareBtn);

    card.append(title, topic, participants, meta, actions);
    grid.appendChild(card);
  });

  return grid;
}

function createScreenSharePanel() {
  const block = document.createElement('section');
  block.className = 'activity-block screen-share';

  const heading = document.createElement('h2');
  heading.textContent = 'Screen Share';

  const screen = document.createElement('div');
  screen.className = 'screen-feed';
  screen.innerHTML = `
    <div class="screen-status">${screenShare.isLive ? 'LIVE' : 'OFFLINE'}</div>
    <div>
      <p>${screenShare.topic}</p>
      <small>by ${screenShare.host}</small>
    </div>
  `;

  const chips = document.createElement('div');
  chips.className = 'participant-chips';

  screenShare.viewers.forEach((viewer) => {
    const chip = document.createElement('span');
    chip.className = 'chip';
    chip.textContent = viewer;
    chips.appendChild(chip);
  });

  const cta = document.createElement('button');
  cta.className = 'button primary';
  cta.textContent = screenShare.isLive ? 'Join Stream' : 'Start Screen Share';
  cta.addEventListener('click', () => {
    controlState.screenSharing = !controlState.screenSharing;
    updateFloatingControls();
  });

  block.append(heading, screen, chips, cta);
  return block;
}

function createActivityPanel() {
  const panel = document.createElement('aside');
  panel.className = 'activity-panel';

  const timelineBlock = document.createElement('section');
  timelineBlock.className = 'activity-block';

  const timelineTitle = document.createElement('h2');
  timelineTitle.textContent = 'Activity';
  timelineBlock.appendChild(timelineTitle);

  const timeline = document.createElement('div');
  timeline.className = 'timeline';

  activityFeed.forEach((entry) => {
    const row = document.createElement('div');
    row.className = 'timeline-entry';

    const marker = document.createElement('div');
    marker.className = 'timeline-marker';
    marker.textContent = entry.marker;

    const content = document.createElement('div');
    content.className = 'timeline-content';
    content.innerHTML = `<strong>${entry.title}</strong><br /><span>${entry.detail} • ${entry.time}</span>`;

    row.append(marker, content);
    timeline.appendChild(row);
  });

  timelineBlock.appendChild(timeline);

  panel.appendChild(timelineBlock);
  panel.appendChild(createScreenSharePanel());

  return panel;
}

function createMainStage() {
  const stage = document.createElement('main');
  stage.className = 'main-stage';

  const voiceSection = document.createElement('section');
  voiceSection.className = 'stage-section';

  const sectionHeader = document.createElement('header');
  sectionHeader.className = 'section-head';
  sectionHeader.innerHTML = `
    <div>
      <h2>Voice Rooms</h2>
      <p class="subtitle">Jump into a live room or browse the schedule</p>
    </div>
  `;
  voiceSection.appendChild(sectionHeader);
  voiceSection.appendChild(createVoiceCards());

  stage.appendChild(voiceSection);
  return stage;
}

function createFloatingControls() {
  const controls = document.createElement('div');
  controls.className = 'floating-controls';
  controls.id = 'floating-controls';

  const micBtn = document.createElement('button');
  micBtn.id = 'ctrl-mic';
  micBtn.innerHTML = '🎤';
  micBtn.addEventListener('click', () => {
    controlState.micMuted = !controlState.micMuted;
    updateFloatingControls();
  });

  const deafenBtn = document.createElement('button');
  deafenBtn.id = 'ctrl-deafen';
  deafenBtn.innerHTML = '🎧';
  deafenBtn.addEventListener('click', () => {
    controlState.deafened = !controlState.deafened;
    updateFloatingControls();
  });

  const screenBtn = document.createElement('button');
  screenBtn.id = 'ctrl-screen';
  screenBtn.innerHTML = '🖥️';
  screenBtn.addEventListener('click', () => {
    controlState.screenSharing = !controlState.screenSharing;
    updateFloatingControls();
  });

  const leaveBtn = document.createElement('button');
  leaveBtn.className = 'danger';
  leaveBtn.innerHTML = '⏻';
  leaveBtn.addEventListener('click', () => openModal('leave'));

  controls.append(micBtn, deafenBtn, screenBtn, leaveBtn);
  return controls;
}

function updateFloatingControls() {
  const micBtn = document.getElementById('ctrl-mic');
  const deafenBtn = document.getElementById('ctrl-deafen');
  const screenBtn = document.getElementById('ctrl-screen');

  if (micBtn) {
    micBtn.style.background = controlState.micMuted
      ? 'rgba(240, 71, 71, 0.9)'
      : 'rgba(255, 255, 255, 0.06)';
    micBtn.title = controlState.micMuted ? 'Unmute' : 'Mute';
  }

  if (deafenBtn) {
    deafenBtn.style.background = controlState.deafened
      ? 'rgba(240, 71, 71, 0.9)'
      : 'rgba(255, 255, 255, 0.06)';
    deafenBtn.title = controlState.deafened ? 'Undeafen' : 'Deafen';
  }

  if (screenBtn) {
    screenBtn.style.background = controlState.screenSharing
      ? 'rgba(88, 101, 242, 0.4)'
      : 'rgba(255, 255, 255, 0.06)';
    screenBtn.title = controlState.screenSharing ? 'Stop Share' : 'Start Share';
  }
}

function toggleJoinRoom(roomId) {
  const room = voiceRooms.find((r) => r.id === roomId);
  if (!room) return;
  room.isJoined = !room.isJoined;
  render();
}

function openModal(type, context) {
  closeModal();

  const backdrop = document.createElement('div');
  backdrop.className = 'modal-backdrop';
  backdrop.addEventListener('click', closeModal);

  const modal = document.createElement('div');
  modal.className = 'modal';
  modal.addEventListener('click', (event) => event.stopPropagation());

  if (type === 'create-room') {
    modal.innerHTML = `
      <h2>Create Voice Room</h2>
      <form id="create-room-form">
        <label>
          Room Name
          <input type="text" name="roomName" placeholder="EG: Design Review" required />
        </label>
        <label>
          Room Type
          <select name="roomType">
            <option value="focus">Focus</option>
            <option value="hangout">Hangout</option>
            <option value="stage">Stage</option>
          </select>
        </label>
        <div class="button-row">
          <button type="button" class="button muted" id="cancel-create">Cancel</button>
          <button type="submit" class="button primary">Create Room</button>
        </div>
      </form>
    `;
  } else if (type === 'invite') {
    modal.innerHTML = `
      <h2>Invite Teammates</h2>
      <p>Share an invite link or search teammates to pull into Innovation Hub.</p>
      <form id="invite-form">
        <label>
          Copy Link
          <input type="url" value="https://echosphere.app/r/innovation" readonly />
        </label>
        <label>
          Search teammates
          <input type="search" placeholder="Type a name..." />
        </label>
        <div class="button-row">
          <button type="button" class="button muted" id="cancel-invite">Close</button>
          <button type="submit" class="button primary">Send Invites</button>
        </div>
      </form>
    `;
  } else if (type === 'share') {
    modal.innerHTML = `
      <h2>Share Screen to ${context}</h2>
      <form id="share-form">
        <label>
          Pick a window
          <select>
            <option>Concept Deck v4 - Figma</option>
            <option>Daily Standup - Notion</option>
            <option>Prototype Demo - Browser</option>
          </select>
        </label>
        <div class="button-row">
          <button type="button" class="button muted" id="cancel-share">Cancel</button>
          <button type="submit" class="button primary">Start Sharing</button>
        </div>
      </form>
    `;
  } else if (type === 'stage') {
    modal.innerHTML = `
      <h2>Launch Stage</h2>
      <p>Stage mode is perfect for all-hands, live AMAs, and official updates.</p>
      <form id="stage-form">
        <label>
          Topic
          <input type="text" placeholder="EG: Roadmap AMA" />
        </label>
        <label>
          Host
          <input type="text" placeholder="Choose a host" />
        </label>
        <div class="button-row">
          <button type="button" class="button muted" id="cancel-stage">Cancel</button>
          <button type="submit" class="button primary">Go Live</button>
        </div>
      </form>
    `;
  } else if (type === 'leave') {
    modal.innerHTML = `
      <h2>Leave Innovation Hub?</h2>
      <p>You'll disconnect from audio and screen sharing.</p>
      <div class="button-row">
        <button type="button" class="button muted" id="cancel-leave">Stay</button>
        <button type="button" class="button primary" id="confirm-leave">Leave Room</button>
      </div>
    `;
  }

  backdrop.appendChild(modal);
  document.body.appendChild(backdrop);

  modal.querySelectorAll('button[id^="cancel"]').forEach((btn) => {
    btn.addEventListener('click', closeModal);
  });

  const confirmLeave = modal.querySelector('#confirm-leave');
  if (confirmLeave) {
    confirmLeave.addEventListener('click', () => {
      controlState.micMuted = true;
      controlState.screenSharing = false;
      updateFloatingControls();
      closeModal();
    });
  }

  modal.querySelectorAll('form').forEach((form) => {
    form.addEventListener('submit', (event) => {
      event.preventDefault();
      closeModal();
    });
  });
}

function closeModal() {
  const existing = document.querySelector('.modal-backdrop');
  if (existing) {
    existing.remove();
  }
}

function render() {
  const root = document.getElementById('app');
  root.innerHTML = '';

  root.appendChild(createServerSidebar());
  root.appendChild(createHeader());
  root.appendChild(createChannelsPanel());
  root.appendChild(createMainStage());
  root.appendChild(createActivityPanel());

  if (!document.getElementById('floating-controls')) {
    document.body.appendChild(createFloatingControls());
  }

  updateFloatingControls();
}

render();
