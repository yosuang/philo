# Layout differences from the reference shots

The chat and details screenshots are appearance references only. Philo keeps its own name, slogan, and live server data.

- The mark is a Philo letter, and the slogan stays “Your agent, your friend. Always on for you.” The reference product name, avatar, and private conversation text are not used.
- The sidebar lists real bots from the server. Each row has an initial, the bot's name, a one-line summary of its latest message, and a selected background. It does not list sessions, and it does not copy the reference’s other chats.
- The details column shows the selected bot’s initial, the live chat state (idle, streaming, or no messages), and the Makez connector status the server reports. It does not show “Ready for review”, “Asleep”, or a voice name as if those were results.
- Search, library, voice, suggested skills, routines, notifications, memory, attachments, and tool-call transcripts stay marked not available.
- Approvals and tasks stay in the chat column so approve and reject work with the details column closed.
- The development-adapter banner stays on screen when the server is in dev mode.
- The window is about 1024×748. The reference frames are 1040×760. The sidebar is 248px and the details column is 268px so the third column fits.
- A short transcript leaves empty space above the latest messages. The details column scrolls when More (notifications, memory, attachments, tool calls, settings, connectors) does not fit.
- Linux GTK draws the system title bar. The reference’s window buttons are not redrawn. macOS and Windows were not run.
