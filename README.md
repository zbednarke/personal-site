# zachbednarke.com

Personal site: [zachbednarke.com](https://zachbednarke.com).

The hero is my name written as a function on the sphere and rebuilt from
its spherical-harmonic expansion as you scroll: 8,281 coefficients
synthesized live in the browser with a stable Legendre recurrence,
checked against SciPy reference values on every page load, and rendered
as a dot lattice on a 2D canvas. No WebGL, no frameworks, no build step.

- `index.html`: the site
- `assets/zach/sh.js`: real spherical-harmonic synthesis in JS
- `assets/zach/widget.js`: the scroll-driven renderer
- `assets/zach/data.js`: coefficients and verification reference values
- `jazz/index.html`: The Jazz Project dashboard at `/jazz/`
- `assets/jazz/`: campaign data, interactions, styles, and social preview

The Jazz Project is also framework-free. Progress is stored in the browser and
can be exported or imported as JSON. The page tracks practice time, skill-tree
levels, repertoire stages, scene milestones, and real-world boss fights.

The live `/jazz/` route is protected by Caddy HTTP Basic Authentication.
`deploy/Caddyfile.jazz.example` documents the path matcher and privacy headers;
the live configuration reuses the existing Portal credential hash. Passwords
and hashes do not belong in this repository.
