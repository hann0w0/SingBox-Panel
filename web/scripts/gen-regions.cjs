const fs = require('fs')
const c = require('i18n-iso-countries')
c.registerLocale(require('i18n-iso-countries/langs/en.json'))
c.registerLocale(require('i18n-iso-countries/langs/zh.json'))
const geo = JSON.parse(fs.readFileSync('src/assets/world.json', 'utf8'))

// bbox center as a cheap centroid for scatter placement
function bboxCenter(geom) {
  let minX = 180, minY = 90, maxX = -180, maxY = -90
  const walk = (co) => {
    if (typeof co[0] === 'number') {
      const [x, y] = co
      if (x < minX) minX = x
      if (x > maxX) maxX = x
      if (y < minY) minY = y
      if (y > maxY) maxY = y
    } else co.forEach(walk)
  }
  walk(geom.coordinates)
  return [(minX + maxX) / 2, (minY + maxY) / 2]
}
const geoCentroid = {}
for (const f of geo.features) geoCentroid[f.properties.name] = bboxCenter(f.geometry)
const geoNames = new Set(Object.keys(geoCentroid))

const alias = {
  'United States of America': 'United States',
  'Russian Federation': 'Russia',
  'Korea, Republic of': 'Korea',
  'Korea (Republic of)': 'Korea',
  'South Korea': 'Korea',
  "Korea, Democratic People's Republic of": 'Dem. Rep. Korea',
  'North Korea': 'Dem. Rep. Korea',
  'Viet Nam': 'Vietnam',
  'Czechia': 'Czech Republic',
  'Taiwan, Province of China': 'China',
  "People's Republic of China": 'China',
  'Hong Kong': 'China',
  'Macao': 'China',
  'United Kingdom of Great Britain and Northern Ireland': 'United Kingdom',
  'Iran, Islamic Republic of': 'Iran',
  'Islamic Republic of Iran': 'Iran',
  'Tanzania, United Republic of': 'Tanzania',
  'United Republic of Tanzania': 'Tanzania',
  'Venezuela, Bolivarian Republic of': 'Venezuela',
  'Bolivia, Plurinational State of': 'Bolivia',
  'Moldova, Republic of': 'Moldova',
  'Syrian Arab Republic': 'Syria',
  "Lao People's Democratic Republic": 'Laos',
  'Türkiye': 'Turkey',
  'Czech Republic': 'Czech Republic',
  'Republic of the Congo': 'Congo',
  'Democratic Republic of the Congo': 'Dem. Rep. Congo',
  'Brunei Darussalam': 'Brunei',
  'Bosnia and Herzegovina': 'Bosnia and Herz.',
  'Central African Republic': 'Central African Rep.',
  'Dominican Republic': 'Dominican Rep.',
  'Equatorial Guinea': 'Eq. Guinea',
  "Cote d'Ivoire": "Côte d'Ivoire",
  'Republic of The Gambia': 'Gambia',
  'The Republic of North Macedonia': 'Macedonia',
  'State of Palestine': 'Palestine',
  'Eswatini': 'Swaziland',
  'South Sudan': 'S. Sudan',
  'Solomon Islands': 'Solomon Is.',
  'Falkland Islands (Malvinas)': 'Falkland Is.',
  'Western Sahara': 'W. Sahara',
}

function matchGeo(en) {
  if (geoNames.has(en)) return en
  if (alias[en] && geoNames.has(alias[en])) return alias[en]
  const trimmed = en.replace(/,.*$/, '').replace(/ \(.*\)/, '').trim()
  if (geoNames.has(trimmed)) return trimmed
  return null
}

const out = {}
const missed = []
for (const code of Object.keys(c.getAlpha2Codes())) {
  const en = c.getName(code, 'en')
  const zh = c.getName(code, 'zh') || en
  if (!en) continue
  const g = matchGeo(en)
  if (!g) { missed.push(code + ':' + en); continue }
  out[code] = { geo: g, coord: geoCentroid[g], label: zh }
}
// City-center coords for small regions that share a country's fill polygon,
// plus overrides for countries whose bbox center is wrong (spanning the
// antimeridian or having far-flung territories: US→Alaska, etc).
const cityCoord = {
  HK: [114.1, 22.3], MO: [113.5, 22.2], TW: [121, 23.7], SG: [103.8, 1.35],
  US: [-98, 39], JP: [138, 37], RU: [90, 61], FR: [2.3, 46.6],
  GB: [-1.5, 52.5], NL: [5.3, 52.1], CA: [-106, 56], NZ: [172, -41],
  CZ: [15.5, 49.8], KR: [127.8, 36.5], AU: [134, -25], NO: [9, 61],
  PT: [-8, 39.5], CL: [-71, -35], EC: [-78.5, -1.5], KI: [173, 1.4],
}
for (const [code, coord] of Object.entries(cityCoord)) {
  if (out[code]) out[code].coord = coord
}
// CZ isn't matched (GeoJSON uses "Czech Rep."); add it explicitly.
if (!out.CZ && geoCentroid['Czech Rep.']) {
  out.CZ = { geo: 'Czech Rep.', coord: [15.5, 49.8], label: c.getName('CZ', 'zh') || 'Czechia' }
} else if (!out.CZ) {
  out.CZ = { geo: 'Czech Republic', coord: [15.5, 49.8], label: c.getName('CZ', 'zh') || 'Czechia' }
}

fs.writeFileSync('src/assets/regions.json', JSON.stringify(out))
console.log('matched', Object.keys(out).length, 'missed', missed.length)
console.log('missed:', missed.join(', '))
console.log('MD:', JSON.stringify(out.MD))
console.log('IT:', JSON.stringify(out.IT))
console.log('ES:', JSON.stringify(out.ES))
console.log('UA:', JSON.stringify(out.UA))
console.log('HU:', JSON.stringify(out.HU))
