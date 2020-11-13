# Generated by the protocol buffer compiler.  DO NOT EDIT!
# source: udpa/annotations/status.proto

require 'google/protobuf'

Google::Protobuf::DescriptorPool.generated_pool.build do
  add_file("udpa/annotations/status.proto", :syntax => :proto3) do
    add_message "udpa.annotations.StatusAnnotation" do
      optional :work_in_progress, :bool, 1
      optional :package_version_status, :enum, 2, "udpa.annotations.PackageVersionStatus"
    end
    add_enum "udpa.annotations.PackageVersionStatus" do
      value :UNKNOWN, 0
      value :FROZEN, 1
      value :ACTIVE, 2
      value :NEXT_MAJOR_VERSION_CANDIDATE, 3
    end
  end
end

module Udpa
  module Annotations
    StatusAnnotation = ::Google::Protobuf::DescriptorPool.generated_pool.lookup("udpa.annotations.StatusAnnotation").msgclass
    PackageVersionStatus = ::Google::Protobuf::DescriptorPool.generated_pool.lookup("udpa.annotations.PackageVersionStatus").enummodule
  end
end