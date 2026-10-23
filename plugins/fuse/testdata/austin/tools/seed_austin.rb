# Seeds the SANDBOX copy of Fuse (/tmp/fuse-sandbox) with a SYNTHETIC Austin
# Mule Box rate book. Never run against ~/Projects/mbc/fuse or any real
# tenant. The ZIPs are real Austin ZIPs; every price, zone, fee and discount
# is invented so the book exercises every branch of QuoteFeeCalculator,
# MileagePricingService and Product#applicable_discount — there is no Austin
# tenant data on this machine to export instead.
#
#   cd /tmp/fuse-sandbox && mise exec -- bundle exec rails runner /tmp/fuse-golden-tools/seed_austin.rb

TENANT = "austin"
begin
  ApplicationRecord.create_tenant(TENANT)
rescue ActiveRecord::Tenanted::TenantExistsError
end

ApplicationRecord.with_tenant(TENANT) do
  PaperTrail.enabled = false
  [ ZipCode, Zone, Product, FeeMileageTier, Fee, MileageTier, Location, ProductType, ServiceType, Discount, Setting, ApiKey ].each(&:delete_all)
  %w[fee_from_zones fee_to_zones fees_service_types fees_product_types products_service_types].each do |t|
    ActiveRecord::Base.connection_pool # noop
    ApplicationRecord.connection.execute("DELETE FROM #{t}")
  end

  Setting.create!(
    name: "Mule Box Austin", domain: "austin.mulebox.com", phone: "(512) 555-0100",
    email_recipients: "ops@example.test", zip: "78747",
    mileage_based_pricing: true, mileage_round_trip: true, zip_qualification: true,
    out_of_area_zip_text: "We don't serve that ZIP yet. Call (512) 555-0100 and we'll see what we can do.",
    quote_validity_days: 30, pricing_enabled: true, total_price_label: "Due Before Delivery"
  )

  d = ->(name, type, amount, unit = :indefinite, value = nil) {
    Discount.create!(name: name, discount_type: type, amount: amount, duration_unit: unit, duration_value: value, description: name)
  }
  first_month = d.("First Month $50 Off", :fixed, 5000, :months, 1)
  downtown    = d.("Downtown 15%", :percentage, 15)
  south_side  = d.("South Side $25 Off", :fixed, 2500, :months, 2)
  storage_sp  = d.("Storage Special 20%", :percentage, 20, :months, 3)
  container   = d.("Container Promo", :fixed, 10000, :years, 1)
  yard_locals = d.("Yard Locals 10%", :percentage, 10, :weeks, 6)
  delivery_33 = d.("Delivery Promo 33%", :percentage, 33)
  fee_5_off   = d.("Fuel $5 Off", :fixed, 500)

  st = ->(name, discount = nil) { ServiceType.create!(name: name, description: name, discount: discount) }
  keep  = st.("Keep It")
  move  = st.("Move It")
  store = st.("Store It", storage_sp)

  pt = ->(name, discount = nil) { ProductType.create!(name: name, description: name, discount: discount) }
  portable = pt.("Portable Storage")
  shipping = pt.("Shipping Container", container)

  # Global tiers, deliberately positioned out of min_miles order: the "Starting
  # at" amount (Fee#effective_amount: order(:position).order(:min_miles)) and
  # the no-result relocation line (MileageTier.order(:min_miles).first) then
  # disagree, 9900 vs 7900.
  MileageTier.create!(min_miles: 15, max_miles: 30, amount: 9900, rate_type: "flat")
  MileageTier.create!(min_miles: 0, max_miles: 15, amount: 7900, rate_type: "flat")
  MileageTier.create!(min_miles: 30, max_miles: 60, amount: 325, rate_type: "per_mile")
  MileageTier.create!(min_miles: 60, max_miles: 400, amount: 8500, rate_type: "per_hour")

  fee = ->(name, amount, **o) {
    services = o.delete(:services) || []
    products = o.delete(:product_types) || []
    from = o.delete(:from) || []
    to = o.delete(:to) || []
    tiers = o.delete(:tiers) || []
    f = Fee.new(name: name, amount: amount, **o)
    tiers.each { |t| f.fee_mileage_tiers.build(t) }
    f.save!
    f.service_types = services
    f.product_types = products
    f.from_zones = from
    f.to_zones = to
    f
  }

  austin = Location.create!(name: "Austin", phone: "5125550100", description: "Austin yard")
  yard_fee = fee.("Yard Access Fee", 1500)
  georgetown = Location.create!(name: "Georgetown Yard", phone: "5125550199", description: "North yard", fee: yard_fee, discount: yard_locals)

  zone = ->(name, location, discount = nil) { Zone.create!(name: name, location: location, discount: discount, description: name) }
  central = zone.("Central", austin)
  south   = zone.("South", austin, south_side)
  north   = zone.("North", georgetown)
  west    = zone.("West", austin)

  all3 = [ keep, move, store ]
  fee.("Initial Delivery", 9900, services: all3)                                  # collides with the live mileage line
  fee.("Final Pickup", 9900, services: [ keep, move ])                            # collides too
  fuel = fee.("Fuel Surcharge", 1500, services: all3, discount: fee_5_off)        # discounted, per quantity
  fee.("Hill Country Delivery", 4500, services: all3, from: [ west ])             # zone-restricted
  fee.("Hill Country Delivery", 3500, services: all3, from: [ north ])            # same name, cheaper: the placeholder
  fee.("Damage Waiver", 1500, services: all3, recurring: true)
  fee.("Storage Facility Handling", 2500, services: [ store ], total_mode: :starting_at)
  fee.("Stairs and Long Carry", 3000, services: [ keep ], total_mode: :excluded_shown)
  fee.("Permit Assistance", 2000, services: [ keep, move ], total_mode: :starting_at_included)
  fee.("Admin Fee", 999, services: all3, total_mode: :excluded)
  fee.("Relocation", 14900, services: [ move ], applies_at: :zone_to_zone, from: [ central ], to: [ north ])
  fee.("Relocation", 12900, services: [ move ], applies_at: :zone_to_zone, to: [ south ])
  fee.("Cross-Town Move", 9900, services: [ move ], applies_at: :zone_to_zone, from: [ central ])
  fee.("Destination Delivery", 2500, services: [ move ], applies_at: :delivery_location, to: [ north ])
  gate = fee.("Destination Gate Fee", 1200, applies_at: :delivery_location)
  gated = fee.("Gated Community Fee", 2000)
  fee.("Long Distance Handling", 0, services: [ store ], mileage_based_pricing: true, discount: delivery_33,
       tiers: [ { min_miles: 0, max_miles: 50, amount: 200, rate_type: "per_mile" },
                { min_miles: 50, max_miles: 500, amount: 150, rate_type: "per_mile" } ])
  fee.("Hill Country Drop-off", 4000, applies_at: :delivery_location, to: [ west ])
  fee.("Container Delivery", 19900, services: [ keep ], product_types: [ shipping ])
  fee.("Container Pickup", 14900, services: [ keep ], product_types: [ shipping ], applies_at: :delivery_location)
  # Zone-restricted and mileage-based: with a zone it's priced from its tiers;
  # in a ZIP with no zone the service placeholder shows fee.amount (1234),
  # not effective_amount (5000) — the zone-to-zone placeholder would.
  fee.("Hill Country Mileage", 1234, services: [ store ], from: [ west ], mileage_based_pricing: true,
       exclude_beyond_last_tier: true,
       tiers: [ { min_miles: 0, max_miles: 100, amount: 5000, rate_type: "flat" },
                { min_miles: 100, max_miles: 300, amount: 9000, rate_type: "flat" } ])
  # A credit under a percentage discount: Integer division floors toward
  # -infinity (-1001 * 33 / 100 = -331), Go's / would truncate (-330).
  fee.("Loyalty Credit", -1001, services: [ keep ], discount: delivery_33)
  # Excluded, zone-to-zone, no destination zones: the exact branch skips it,
  # the "Starting at" branch doesn't.
  fee.("Long Haul Move", 5000, services: [ move ], applies_at: :zone_to_zone, from: [ central ], total_mode: :excluded)
  # A position of nil: Ruby's sort_by puts it last (Float::INFINITY), SQL's
  # ORDER BY position puts it first.
  fuel.update_column(:position, nil)

  prod = ->(name, price, **o) {
    services = o.delete(:services)
    p = Product.create!(name: name, price: price, **o)
    p.service_types = services
    p
  }
  prod.("8' Mule Box", 14900, product_type: portable, services: all3, discount: first_month, monthly: true, length: 8, width: 7, height: 7,
        ideal_for_1: "A dorm room", ideal_for_2: "Seasonal storage")
  prod.("12' Mule Box", 17900, product_type: portable, services: all3, monthly: true, length: 12, width: 7, height: 7, ideal_for_1: "1-2 bedrooms")
  prod.("16' Mule Box", 19900, product_type: portable, services: [ keep, move ], monthly: true, length: 16, width: 8, height: 8, ideal_for_1: "3 bedrooms")
  prod.("20' Mule Box", 23900, product_type: portable, services: all3, monthly: true, active: false)
  prod.("20' Container Rental", 12000, product_type: shipping, services: [ keep ], monthly: true, length: 20, width: 8, height: 8)
  prod.("20' Container (Sale)", 300000, product_type: shipping, services: [ keep ], for_sale: true, length: 20, width: 8, height: 8)
  prod.("40' Container (Sale)", 450000, product_type: shipping, services: [ keep ], for_sale: true, length: 40, width: 8, height: 8)

  z = ->(code, zone_rec, location, fee: nil, discount: nil) {
    ZipCode.create!(postal_code: code, zone: zone_rec, location: location, fee: fee, discount: discount)
  }
  z.("78701", central, austin)
  z.("78702", central, austin)
  z.("78703", central, austin, discount: downtown)
  z.("78704", south, austin)
  z.("78745", south, austin)
  z.("78747", south, austin)
  z.("78748", south, austin, fee: gated)
  z.("78749", south, austin)
  z.("78613", north, georgetown)
  z.("78626", north, georgetown)
  z.("78664", north, georgetown)
  z.("78660", north, georgetown, fee: gate)
  z.("78738", west, austin)
  z.("78746", west, austin)
  z.("78620", west, austin)
  z.("78610", nil, austin)                  # in the table, no zone: the "Starting at" paths
  z.("78640-1234", south, austin)           # ZIP+4 only: served, but the calculator's exact match misses it


  puts "seeded: #{Fee.count} fees, #{Product.count} products, #{ZipCode.count} zips, #{MileageTier.count} tiers"
end
