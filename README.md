# zbednarke.github.io

Personal site: [zbednarke.github.io](https://zbednarke.github.io).

The hero is my name written as a function on the sphere and rebuilt from
its spherical-harmonic expansion as you scroll: 8,281 coefficients
synthesized live in the browser with a stable Legendre recurrence,
checked against SciPy reference values on every page load, and rendered
as a dot lattice on a 2D canvas. No WebGL, no frameworks, no build step.

- `index.html`: the site
- `assets/zach/sh.js`: real spherical-harmonic synthesis in JS
- `assets/zach/widget.js`: the scroll-driven renderer
- `assets/zach/data.js`: coefficients and verification reference values
