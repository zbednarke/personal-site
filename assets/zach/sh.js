// Real spherical-harmonic synthesis in the browser.
//
// Mirrors the Python zharm conventions exactly:
//   theta = colatitude, phi = azimuth
//   Ptilde_lm = N_lm * P_l^m(cos theta), Condon-Shortley phase included
//   (the minus sign in the m,m recurrence), so that the complex
//   Y_l^m = Ptilde_lm * e^{i m phi} matches scipy.special.sph_harm_y.
//   Real harmonics:
//     Y_{l,0}   = Ptilde_l0
//     Y_{l,m>0} = sqrt(2) (-1)^m Ptilde_lm cos(m phi)
//     Y_{l,-m}  = sqrt(2) (-1)^m Ptilde_lm sin(m phi)
//   Coefficient ordering: k = l*(l+1) + m.
//
// The stable column recurrence (increasing l at fixed m):
//   Ptilde_00     = sqrt(1/4pi)
//   Ptilde_mm     = -sqrt((2m+1)/(2m)) sin(theta) Ptilde_{m-1,m-1}
//   Ptilde_{m+1,m} = sqrt(2m+3) cos(theta) Ptilde_mm
//   Ptilde_lm     = a_l ( cos(theta) Ptilde_{l-1,m} - Ptilde_{l-2,m} / a_{l-1} )
//     with a_l = sqrt( (4l^2 - 1) / (l^2 - m^2) )

(() => {
  "use strict";

  // Ptilde_lm(cos theta) for all 0 <= m <= l <= lmax at one theta.
  // Returns Float64Array indexed by pair(l, m) = l*(l+1)/2 + m.
  function legendreColumn(lmax, theta) {
    const n = ((lmax + 1) * (lmax + 2)) / 2;
    const P = new Float64Array(n);
    const x = Math.cos(theta);
    const s = Math.sin(theta);
    const pair = (l, m) => (l * (l + 1)) / 2 + m;

    P[0] = Math.sqrt(1 / (4 * Math.PI));
    for (let m = 1; m <= lmax; m++) {
      P[pair(m, m)] =
        -Math.sqrt((2 * m + 1) / (2 * m)) * s * P[pair(m - 1, m - 1)];
    }
    for (let m = 0; m < lmax; m++) {
      P[pair(m + 1, m)] = Math.sqrt(2 * m + 3) * x * P[pair(m, m)];
    }
    for (let m = 0; m <= lmax; m++) {
      let aPrev = Math.sqrt(
        (4 * (m + 1) * (m + 1) - 1) / ((m + 1) * (m + 1) - m * m)
      );
      for (let l = m + 2; l <= lmax; l++) {
        const a = Math.sqrt((4 * l * l - 1) / (l * l - m * m));
        P[pair(l, m)] =
          a * (x * P[pair(l - 1, m)] - P[pair(l - 2, m)] / aPrev);
        aPrev = a;
      }
    }
    return P;
  }

  // Per-degree contribution fields on a (thetas x phis) lattice.
  // Returns { deg, cum }: arrays of length lmax+1 of Float64Array(nTh*nPh),
  // where cum[L] is the partial sum through degree L.
  function buildFields(coeffs, lmax, thetas, phis) {
    const nTh = thetas.length;
    const nPh = phis.length;
    const pair = (l, m) => (l * (l + 1)) / 2 + m;

    const Ptab = thetas.map((th) => legendreColumn(lmax, th)); // [nTh][pairs]

    const cosM = [];
    const sinM = [];
    for (let m = 0; m <= lmax; m++) {
      const c = new Float64Array(nPh);
      const s = new Float64Array(nPh);
      for (let j = 0; j < nPh; j++) {
        c[j] = Math.cos(m * phis[j]);
        s[j] = Math.sin(m * phis[j]);
      }
      cosM.push(c);
      sinM.push(s);
    }

    const deg = [];
    const cum = [];
    for (let l = 0; l <= lmax; l++) {
      const field = new Float64Array(nTh * nPh);
      for (let i = 0; i < nTh; i++) {
        const P = Ptab[i];
        const row = i * nPh;
        // m = 0
        const c0 = coeffs[l * (l + 1)] * P[pair(l, 0)];
        for (let j = 0; j < nPh; j++) field[row + j] += c0;
        // m > 0
        for (let m = 1; m <= l; m++) {
          const sgn = m % 2 ? -Math.SQRT2 : Math.SQRT2; // sqrt(2)(-1)^m
          const plm = sgn * P[pair(l, m)];
          const ac = coeffs[l * (l + 1) + m] * plm;
          const as = coeffs[l * (l + 1) - m] * plm;
          const cm = cosM[m];
          const sm = sinM[m];
          for (let j = 0; j < nPh; j++) {
            field[row + j] += ac * cm[j] + as * sm[j];
          }
        }
      }
      deg.push(field);
      const c = Float64Array.from(cum.length ? cum[l - 1] : field.map(() => 0));
      for (let j = 0; j < field.length; j++) c[j] += field[j];
      cum.push(c);
    }
    return { deg, cum, nTh, nPh };
  }

  // Verify against the Python-exported reference partial sums.
  // Returns the max abs error across all exported degrees/points.
  function selfTest(data, fields) {
    const { theta_idx, phi_idx, values } = data.reference;
    let maxErr = 0;
    for (const [Lstr, vals] of Object.entries(values)) {
      const cum = fields.cum[Number(Lstr)];
      let k = 0;
      for (const ti of theta_idx) {
        for (const pj of phi_idx) {
          const err = Math.abs(cum[ti * fields.nPh + pj] - vals[k++]);
          if (err > maxErr) maxErr = err;
        }
      }
    }
    return maxErr;
  }

  globalThis.SH = { legendreColumn, buildFields, selfTest };
})();
