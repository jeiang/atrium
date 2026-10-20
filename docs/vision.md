# Vision

atrium should be my agentic personal assistant: something that understands
my circumstances, helps with day-to-day tasks, and can do useful work on
my behalf.

## Bring my information and actions together

I should be able to work with information from the tools and services I
use without repeatedly opening each application or explaining the same
context. atrium should bring those capabilities into one place, while
leaving me free to use the original applications.

The point is not to collect integrations. Each integration should help
the assistant understand something important to me or act on it.

## Build lasting knowledge about my life

atrium should maintain a useful wiki of knowledge about me: my tasks,
interests, friends, work, preferences, and the relationships between them.

That knowledge should persist beyond individual conversations and remain
inspectable and correctable. I should not have to keep rebuilding the
assistant's understanding of my life.

## Help proactively and act on my behalf

With the right knowledge, the assistant should recognize useful work
without waiting for me to spell out every step. It should help me notice
what needs attention, prepare for what is coming, and carry out tasks
within the authority I give it.

I should be able to ask it to download a show, email a friend about
something discussed earlier, or check on a package, without supplying
information it already knows or manually coordinating the underlying
tools.

Proactive assistance should reduce my workload, not create another stream
of information that I have to manage.

## Adapt to different people

atrium is built around my needs, but it should not depend on my identity,
accounts, location, or particular set of services.

Its capabilities should be modular. Other people should be able to use
the parts they need, leave out those they do not, and configure the
assistant for their own circumstances.

## Keep configuration and state independent of the program

Behavior should be configurable rather than requiring source changes.
Mutable state should live outside the program and its source tree, in
systems such as SQLite and Notion, with other backends possible later.

Updating or replacing the program should not mean losing the knowledge,
configuration, or history that makes the assistant useful.

## Protect private information

The assistant will hold information about my life and have access to
important services. Security and privacy must shape its design, including
what it can access, what it can change, and what information leaves the
systems I control.

Useful autonomy depends on permissions I can understand and control.

## Get the best outcome for the lowest practical cost

Efficiency means reducing the cost of useful outcomes, not simply choosing
the cheapest model or minimizing one request.

The harness should make effective use of caching. Agents should have
purpose-built tools for routine work rather than needing to write scripts
to reach ordinary capabilities.

Delegation should divide work where that improves results, avoid
overloading one context window, and preserve useful context and cache
reuse for each agent.
