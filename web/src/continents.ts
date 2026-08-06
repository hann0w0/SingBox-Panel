// Continent grouping for the "分段大区" node picker (replaces the ECharts map).
// Covers every ISO-3166 alpha-2 code present in assets/regions.json so new
// regions never fall through to "其他" silently. Unknown codes → "其他".

export const CONTINENT_ORDER = ['亚洲', '欧洲', '美洲', '大洋洲', '非洲', '其他'] as const
export type Continent = (typeof CONTINENT_ORDER)[number]

const C: Record<string, Continent> = {
  // ── 亚洲 ──────────────────────────────────────────────────
  AE: '亚洲', AF: '亚洲', AM: '亚洲', AZ: '亚洲', BD: '亚洲', BH: '亚洲',
  BN: '亚洲', BT: '亚洲', CN: '亚洲', GE: '亚洲', HK: '亚洲', ID: '亚洲',
  IL: '亚洲', IN: '亚洲', IQ: '亚洲', IR: '亚洲', JO: '亚洲', JP: '亚洲',
  KG: '亚洲', KH: '亚洲', KP: '亚洲', KR: '亚洲', KW: '亚洲', KZ: '亚洲',
  LA: '亚洲', LB: '亚洲', LK: '亚洲', MM: '亚洲', MN: '亚洲', MO: '亚洲',
  MY: '亚洲', NP: '亚洲', OM: '亚洲', PH: '亚洲', PK: '亚洲', PS: '亚洲',
  QA: '亚洲', SA: '亚洲', SG: '亚洲', SY: '亚洲', TH: '亚洲', TJ: '亚洲',
  TL: '亚洲', TM: '亚洲', TR: '亚洲', TW: '亚洲', UZ: '亚洲', VN: '亚洲',
  YE: '亚洲',
  // ── 欧洲 ──────────────────────────────────────────────────
  AD: '欧洲', AL: '欧洲', AT: '欧洲', BA: '欧洲', BE: '欧洲', BG: '欧洲',
  BY: '欧洲', CH: '欧洲', CY: '欧洲', CZ: '欧洲', DE: '欧洲', DK: '欧洲',
  EE: '欧洲', ES: '欧洲', FI: '欧洲', FR: '欧洲', GB: '欧洲', GR: '欧洲',
  HR: '欧洲', HU: '欧洲', IE: '欧洲', IS: '欧洲', IT: '欧洲', LI: '欧洲',
  LT: '欧洲', LU: '欧洲', LV: '欧洲', MD: '欧洲', ME: '欧洲', MK: '欧洲',
  MT: '欧洲', NL: '欧洲', NO: '欧洲', PL: '欧洲', PT: '欧洲', RO: '欧洲',
  RS: '欧洲', RU: '欧洲', SE: '欧洲', SI: '欧洲', SK: '欧洲', UA: '欧洲',
  // ── 美洲 ──────────────────────────────────────────────────
  AR: '美洲', BB: '美洲', BM: '美洲', BO: '美洲', BR: '美洲', BS: '美洲',
  BZ: '美洲', CA: '美洲', CL: '美洲', CO: '美洲', CR: '美洲', CU: '美洲',
  CW: '美洲', DM: '美洲', DO: '美洲', EC: '美洲', FK: '美洲', GD: '美洲',
  GL: '美洲', GT: '美洲', GY: '美洲', HN: '美洲', HT: '美洲', JM: '美洲',
  LC: '美洲', MX: '美洲', MS: '美洲', NI: '美洲', PA: '美洲', PE: '美洲',
  PR: '美洲', PY: '美洲', SR: '美洲', SV: '美洲', TT: '美洲', US: '美洲',
  UY: '美洲', VE: '美洲',
  // ── 大洋洲 ────────────────────────────────────────────────
  AS: '大洋洲', AU: '大洋洲', FJ: '大洋洲', FM: '大洋洲', GU: '大洋洲',
  KI: '大洋洲', NC: '大洋洲', NU: '大洋洲', NZ: '大洋洲', PG: '大洋洲',
  PW: '大洋洲', SB: '大洋洲', TO: '大洋洲', VU: '大洋洲', WS: '大洋洲',
  // ── 非洲 ──────────────────────────────────────────────────
  AO: '非洲', BF: '非洲', BI: '非洲', BJ: '非洲', BW: '非洲', CD: '非洲',
  CF: '非洲', CG: '非洲', CI: '非洲', CM: '非洲', CV: '非洲', DJ: '非洲',
  DZ: '非洲', EG: '非洲', EH: '非洲', ER: '非洲', ET: '非洲', GA: '非洲',
  GH: '非洲', GM: '非洲', GN: '非洲', GQ: '非洲', GW: '非洲', KE: '非洲',
  KM: '非洲', LR: '非洲', LS: '非洲', LY: '非洲', MA: '非洲', MG: '非洲',
  ML: '非洲', MR: '非洲', MU: '非洲', MW: '非洲', MZ: '非洲', NA: '非洲',
  NE: '非洲', NG: '非洲', RW: '非洲', SC: '非洲', SD: '非洲', SL: '非洲',
  SN: '非洲', SO: '非洲', SS: '非洲', SZ: '非洲', TD: '非洲', TG: '非洲',
  TN: '非洲', TZ: '非洲', UG: '非洲', ZA: '非洲', ZM: '非洲', ZW: '非洲',
  // ── 其他 ──────────────────────────────────────────────────
  IM: '其他', JE: '其他', SH: '其他',
}

export function continentOf(code: string | undefined): Continent {
  if (!code) return '其他'
  return C[code.toUpperCase()] ?? '其他'
}
