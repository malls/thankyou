# TYB-13: back button when an image is open should go back to the generator page

## Problem

`createImage()` in `server/public/script.js` (lines 284–311) renders the PNG into
the page by doing `document.body.innerHTML = '<div class="scroller"><img.../></div>'`.
An innerHTML replacement is not a navigation, so the browser has no history entry
to pop and the back button doesn't return the user to the generator. They're stuck.

Out of scope: the post-payment thanks view (`renderThanksState`) and the canceled
banner (`renderCanceledState`). Those are reached via Stripe redirect, so the
browser back button correctly goes "back to Stripe" for those flows.

## Chosen approach: A — Overlay (no innerHTML replacement)

Keep the generator DOM intact; show the image inside an overlay div that covers
the page. Push a history entry on show; pop it (or react to popstate) to hide.

Justification: no re-binding of event handlers, no risk of forgetting a listener,
no need to re-run `init()`. Diff is small and the failure mode of approach B
(forgotten listener after restore) is exactly the kind of foot-gun this codebase
tries to avoid.

## Concrete changes

### 1. `server/public/index.html`

Add an overlay container just before `</body>` (kept hidden by default):

```html
<div id="image-view" hidden>
    <img id="image-view-img" alt="Generated thank you image" />
</div>
```

Rationale for putting it in the static HTML rather than creating in JS on demand:
matches the existing pattern for `#buy-error` (declared `hidden` in markup, toggled
by JS). One source of truth for the structure.

### 2. `server/public/style.css`

Add overlay styles. Keep the existing `.scroller` rules — they still apply because
the overlay contains a scrollable area:

```css
#image-view {
    position: fixed;
    inset: 0;
    background: #fff;
    overflow: auto;
    z-index: 1000;
}

#image-view img {
    display: block;
    max-width: 100%;
    height: auto;
}
```

The existing `.scroller` class becomes unused after this change — leave it; deleting
it is out of scope for this fix and risks coupling.

### 3. `server/public/script.js` — `createImage()`

Replace the `image.onload` body so it populates the overlay and shows it instead of
replacing `document.body.innerHTML`. Pseudocode:

```js
image.onload = () => {
    context.drawImage(image, X_IMAGE_OFFSET, 0);
    const png = canvas.toDataURL('image/png');

    const view = document.getElementById('image-view');
    const img  = document.getElementById('image-view-img');
    img.src = png;

    view.hidden = false;

    // Push a history entry so the browser back button has something to pop.
    // Keep the URL identical — the image is not deep-linkable; the state
    // marker is what we react to.
    history.pushState({ view: 'image' }, '', window.location.href);
};
```

### 4. `popstate` handler

Add a single `popstate` listener inside `window.onload`, alongside the other
top-level wiring (right after the `#export` click binding around line 76):

```js
window.addEventListener('popstate', event => {
    // The input keyup handlers push history entries with state === null.
    // Only react when we're popping AWAY from our image view, i.e. the
    // current state is not our image marker. Hide the overlay in that case.
    const view = document.getElementById('image-view');
    if (view && !view.hidden && (!event.state || event.state.view !== 'image')) {
        view.hidden = true;
        const img = document.getElementById('image-view-img');
        if (img) img.removeAttribute('src');
    }
});
```

The check `!view.hidden && state isn't ours` makes this robust against the
existing input-keyup pushes (which use `state === null`). If the user is typing
in the generator and triggers a popstate from typing history, we do nothing
because the overlay is already hidden.

## History state shape

`history.pushState({ view: 'image' }, '', window.location.href)`

- State: `{ view: 'image' }` — a discriminator so `popstate` can tell our entry
  apart from the input-keyup entries (which use `null`).
- URL: unchanged. The image is not deep-linkable and we don't want `?text=...`
  to be mutated by the image action.

## Edge cases

1. **Open image, back to generator, click Save Image again.** Works: each click
   pushes a new history entry; each back pops it. The overlay is re-shown with
   a fresh PNG every time (we set `img.src` unconditionally).

2. **`?session_id=` or `?canceled=1` on the URL when Save Image is clicked.**
   Unaffected. `renderThanksState` returns early before `init()`, so the export
   button isn't bound when a session_id is present. The canceled flow does bind
   the export button — works the same as the no-banner path.

3. **Reload on the image view.** The image is not persisted; the page reloads
   into the generator with text restored from `?text=` / `?middletext=`. The
   user can re-click Save Image. Acceptable per task spec.

4. **Popstate from a state we didn't push.** Input-keyup pushes use
   `state === null`. The handler checks `!view.hidden` first, so unless the
   overlay is currently showing, popstate is a no-op for image-view purposes.
   When the overlay IS showing and a `null`-state pop arrives, that's exactly
   the back button popping our pushed entry — we hide.

5. **User navigates forward to the image view via browser forward button.** Not
   a supported use case; acceptable. The image data is not persisted; the
   forward navigation would re-trigger popstate with our `{view: 'image'}`
   state but the `<img>` src is empty. Not worth handling — the user would
   click Save Image again.

## HTML/CSS changes summary

- `server/public/index.html`: add `<div id="image-view" hidden><img id="image-view-img"/></div>` before `</body>`.
- `server/public/style.css`: add `#image-view` and `#image-view img` rules. Leave `.scroller` alone.

## Tests (manual — project has no JS test harness; do not add one)

1. Open `/`, type "HELLO" in main input. Click Save Image. Expect: image renders,
   covers the page. URL unchanged.
2. Click browser back. Expect: generator re-appears with "HELLO" still in the
   input and rendered. URL unchanged.
3. Click Save Image again. Expect: image renders again. Click back again. Expect:
   generator returns. (Verifies the second-time path.)
4. Type "WORLD" in main input. Verify keyup-driven URL updates (`?text=WORLD`)
   still work and `popstate` from those typing entries doesn't show or hide the
   image view spuriously.

## Acceptance criteria

- Opening the page, typing text, and clicking Save Image shows the rendered
  image in an overlay that covers the page.
- Clicking the browser back button while the image is showing returns the user
  to the generator with their text inputs intact (preserved by the existing
  `?text=` / `?middletext=` query-param round-trip).
- The post-payment thanks view (`?session_id=...`) and canceled banner
  (`?canceled=1`) are unchanged.
- No regression to the input keyup handlers that push `?text=` / `?middletext=`
  history entries.

## Out of scope

- The thanks state (`renderThanksState`) and canceled banner (`renderCanceledState`).
- Adding a JS test framework.
- Refactoring `createImage()` beyond the `image.onload` change required for this fix.
- Deep-linking to a generated image.
- Removing the now-unused `.scroller` CSS class.
