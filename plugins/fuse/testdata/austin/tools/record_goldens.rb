# Records golden pricing cases from Fuse's own Ruby, in the SANDBOX copy
# (/tmp/fuse-sandbox), for the synthetic Austin tenant seeded by
# seed_austin.rb. Every case goes through the real Rails stack in-process
# (ActionDispatch::Integration::Session → Api::V1::Quoting::QuotesController
# #preview / FormsController#site_config / ServiceAreasController#show), so
# the answers are Fuse's, byte for byte.
#
# Google is not called. MileagePricingService.directions is replaced by a
# fixture: synthetic road legs from approximate ZIP centroids (haversine ×
# 1.3, 40 mph). Every route asked for is written to distances.json, which the
# Go tests replay; an unrecorded route fails them.
#
# Also records oracles for the Ruby behaviours the Go port reimplements:
# Float#round(n), Float#to_s, Array#sum (Kahan-Babuska) and sort_by's tie
# order (glibc qsort_r on this machine; see sort.go).
#
#   cd /tmp/fuse-sandbox && mise exec -- bundle exec rails runner /tmp/fuse-golden-tools/record_goldens.rb

require "json"

OUT = ENV.fetch("OUT", "/tmp/fuse-golden-tools/out")
FileUtils.mkdir_p(OUT)
TENANT = "austin"
HOST = "#{TENANT}.localhost"

CENTROIDS = {
  "78747" => [ 30.130, -97.740 ], "78701" => [ 30.271, -97.742 ], "78702" => [ 30.263, -97.714 ],
  "78703" => [ 30.293, -97.765 ], "78704" => [ 30.243, -97.765 ], "78745" => [ 30.207, -97.796 ],
  "78748" => [ 30.161, -97.822 ], "78749" => [ 30.214, -97.857 ], "78613" => [ 30.505, -97.820 ],
  "78626" => [ 30.660, -97.670 ], "78664" => [ 30.511, -97.660 ], "78660" => [ 30.448, -97.596 ],
  "78738" => [ 30.335, -97.980 ], "78746" => [ 30.296, -97.810 ], "78620" => [ 30.220, -98.110 ],
  "78610" => [ 30.080, -97.840 ], "78640" => [ 29.990, -97.870 ], "90210" => [ 34.090, -118.410 ]
}.freeze

$distances = {}
$google = :fixture

def leg(a, b)
  la, lb = CENTROIDS.fetch(a), CENTROIDS.fetch(b)
  r = 6_371_000.0
  p1, p2 = la[0] * Math::PI / 180, lb[0] * Math::PI / 180
  dp = p2 - p1
  dl = (lb[1] - la[1]) * Math::PI / 180
  h = Math.sin(dp / 2)**2 + Math.cos(p1) * Math.cos(p2) * Math.sin(dl / 2)**2
  meters = (2 * r * Math.asin(Math.sqrt(h)) * 1.3).round
  meters = 350 if meters.zero? # same-ZIP trips still drive across the ZIP
  [ meters, (meters / 17.8816).round ]
end

MileagePricingService.singleton_class.class_eval do
  define_method(:api_key) { $google == :no_key ? nil : "fixture-key" }
  define_method(:directions) do |origin, destination, waypoints = []|
    key = [ origin, destination, waypoints.join("|") ].join(" → ")
    return nil if $google == :down
    stops = [ origin, *waypoints, destination ].map { |s| s.delete_suffix(", USA") }
    unless stops.all? { |z| CENTROIDS.key?(z) }
      $distances[key] = { "status" => "ZERO_RESULTS" }
      return nil
    end
    legs = stops.each_cons(2).map { |a, b| leg(a, b) }
    $distances[key] = { "status" => "OK", "legs_meters" => legs.map(&:first), "legs_seconds" => legs.map(&:last) }
    { total_meters: legs.sum(&:first), legs_meters: legs.map(&:first), legs_seconds: legs.map(&:last),
      polyline: nil, origin_coords: nil, dest_coords: nil, waypoint_coords: [] }
  end
end

VARIANTS = {
  "round_trip" => { settings: { mileage_based_pricing: true, mileage_round_trip: true }, google: :fixture },
  "flat"       => { settings: { mileage_based_pricing: false, mileage_round_trip: true }, google: :fixture },
  "one_way"    => { settings: { mileage_based_pricing: true, mileage_round_trip: false }, google: :fixture },
  "google_down" => { settings: { mileage_based_pricing: true, mileage_round_trip: true }, google: :down },
  "no_key"     => { settings: { mileage_based_pricing: true, mileage_round_trip: true }, google: :no_key }
}.freeze

ZIPS = %w[78701 78702 78703 78704 78745 78747 78748 78749 78613 78626 78664 78660 78738 78746 78620 78610 78640 90210 78799].freeze

def preview_cases(variant)
  ids = ApplicationRecord.with_tenant(TENANT) { Product.order(:id).pluck(:id) }
  p8, p12, p16 = ids
  cases = []
  add = ->(**c) { cases << c }
  zips = variant == "round_trip" ? ZIPS : %w[78701 78704 78748 78613 78660 78738 78610 78640 90210]
  zips.each do |zip|
    %w[keep-it store-it move-it].each { |st| add.(service_type: st, product_type: "portable-storage", zip_code: zip) }
  end
  dests = [ [ "78701", "78702" ], [ "78701", "78626" ], [ "78704", "78745" ], [ "78701", "78738" ], [ "78701", "78660" ],
            [ "78613", "78748" ], [ "78610", "78701" ], [ "78701", "78610" ], [ "78701", "90210" ], [ "78701", "78799" ],
            [ "78640", "78701" ], [ "78738", "78620" ], [ "78747", "78747" ] ]
  dests.each do |zip, dest|
    add.(service_type: "move-it", product_type: "portable-storage", zip_code: zip, destination_zip_code: dest)
    add.(service_type: "keep-it", product_type: "portable-storage", zip_code: zip, destination_zip_code: dest) if %w[78626 78660 78738].include?(dest)
    add.(service_type: "store-it", product_type: "portable-storage", zip_code: zip, destination_zip_code: dest) if dest == "78660"
  end
  if variant == "round_trip" || variant == "flat"
    %w[78701 78703 78613 78738].each do |zip|
      add.(service_type: "keep-it", product_type: "shipping-container", zip_code: zip)
      add.(service_type: "keep-it", product_type: "shipping-container", zip_code: zip, destination_zip_code: "78660")
      add.(service_type: "keep-it", product_type: "", zip_code: zip)
      add.(service_type: "move-it", product_type: "shipping-container", zip_code: zip)
      add.(service_type: "bogus", product_type: "portable-storage", zip_code: zip)
    end
    [ [ "78701", { p12.to_s => 2 } ], [ "78703", { p8.to_s => 1, p12.to_s => 1 } ], [ "78748", { p8.to_s => 0 } ],
      [ "78704", { "999999" => 1 } ], [ "78613", { p16.to_s => "2abc" } ], [ "78738", { p8.to_s => 3, p16.to_s => -1 } ],
      [ "78610", { p12.to_s => 2, p16.to_s => 1 } ] ].each do |zip, q|
      %w[keep-it move-it store-it].each { |st| add.(service_type: st, product_type: "portable-storage", zip_code: zip, quantities: q) }
    end
    add.(service_type: "keep-it", product_type: "portable-storage", zip_code: "")
    add.(service_type: "move-it", product_type: "portable-storage", zip_code: "", destination_zip_code: "78701")
    add.(service_type: "", product_type: "portable-storage", zip_code: "78701")
  end
  cases
end

def with_settings(variant)
  v = VARIANTS.fetch(variant)
  ApplicationRecord.with_tenant(TENANT) { Setting.first.update_columns(v[:settings]) }
  Rails.cache.clear
  $google = v[:google]
end

token = ApplicationRecord.with_tenant(TENANT) { ApiKey.delete_all; ApiKey.create!(label: "goldens").token }

session = ActionDispatch::Integration::Session.new(Rails.application)
session.host! HOST
headers = { "Authorization" => "Bearer #{token}", "Content-Type" => "application/json", "Accept" => "application/json" }

goldens = { "previews" => [], "configs" => [], "service_areas" => [] }

VARIANTS.each_key do |variant|
  with_settings(variant)
  preview_cases(variant).each do |c|
    session.post "/api/v1/quoting/preview", params: c.to_json, headers: headers
    raise "preview #{c.inspect}: #{session.response.status} #{session.response.body}" unless session.response.status == 200
    goldens["previews"] << { "variant" => variant, "request" => c, "response" => JSON.parse(session.response.body)["data"] }
  end
end

with_settings("round_trip")
(ZIPS + [ "" ]).each do |zip|
  [ nil, "keep-it", "store-it", "move-it" ].each do |st|
    q = { zip_code: zip, service_type: st }.compact
    session.get "/api/v1/quoting/config", params: q, headers: headers
    raise "config #{q}: #{session.response.status}" unless session.response.status == 200
    data = JSON.parse(session.response.body)["data"]
    goldens["configs"] << { "request" => q, "products" => data["products"], "service_types" => data["service_types"],
                            "product_types" => data["product_types"],
                            "phone" => data.dig("settings", "phone"), "location_phone" => data.dig("settings", "location_phone") }
  end
end

[ true, false ].each do |qualification|
  ApplicationRecord.with_tenant(TENANT) { Setting.first.update_columns(zip_qualification: qualification) }
  Rails.cache.clear
  (ZIPS + [ "78640" ]).uniq.each do |zip|
    session.get "/api/service_areas/#{zip}", headers: { "Accept" => "application/json" }
    goldens["service_areas"] << { "zip_qualification" => qualification, "zip" => zip, "status" => session.response.status,
                                  "response" => JSON.parse(session.response.body) }
  end
end
ApplicationRecord.with_tenant(TENANT) { Setting.first.update_columns(zip_qualification: true) }
Rails.cache.clear

# ---- oracles for the Ruby behaviours the Go port reimplements -----------
rng = Random.new(20261023)
floats = []
2000.times { floats << rng.rand(0.0..400.0) }
500.times { floats << (rng.rand(0..400_00) / 100.0) + 0.005 }       # x.xx5 — the half-way cases
500.times { floats << rng.rand(0..400_000) * 1.0 / 1000 }
300.times { m = rng.rand(1..900_000); floats << m / 1609.344 }       # what the service rounds
300.times { s = rng.rand(1..40_000); floats << s / 3600.0 }
floats += [ 0.0, 0.05, 0.15, 0.25, 0.35, 1.005, 2.675, 1.0e-7, 12.345, 18.85, 0.95, 99.995, 1234.5, 0.0049999999 ]
rounds = floats.map { |f| [ f, f.round(1), f.round(2), f.round(4), f.round(1).to_s, f.round(2).to_s ] }

sums = 300.times.map do
  n = rng.rand(1..4)
  a = n.times.map { (rng.rand(1..300_000) / 1609.344).round(2) }
  [ a, a.sum, a.sum.round(2) ]
end
sums << [ [ 0.1 ] * 10, ([ 0.1 ] * 10).sum, ([ 0.1 ] * 10).sum.round(2) ]

sorts = 4000.times.map do
  n = rng.rand(1..40)
  keys = n.times.map { k = rng.rand(0..5); k == 5 ? nil : k }
  items = keys.each_with_index.map { |k, i| [ i, k ] }
  [ keys, items.sort_by { |_, k| k || Float::INFINITY }.map(&:first) ]
end

mults = 1000.times.map do
  amount = rng.rand(1..20000)
  miles = (rng.rand(0..60_000) / 100.0)
  hours = (rng.rand(0..400_000) / 10000.0)
  [ amount, miles, hours, (amount * miles).round, (amount * [ hours, 1.0 ].max).round ]
end

File.write("#{OUT}/goldens.json", JSON.pretty_generate(goldens))
File.write("#{OUT}/distances.json", JSON.pretty_generate($distances.sort.to_h))
File.write("#{OUT}/ruby_oracles.json", JSON.generate({ "ruby" => RUBY_VERSION, "glibc" => `ldd --version`.lines.first.strip,
  "rounds" => rounds, "sums" => sums, "sorts" => sorts, "mults" => mults }))
puts "previews #{goldens['previews'].size}, configs #{goldens['configs'].size}, service areas #{goldens['service_areas'].size}, routes #{$distances.size}"
