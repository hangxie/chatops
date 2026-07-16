Name:           chatops
Version:        CHANGEME
Release:        1%{?dist}
Summary:        ChatOps bot and tooling
License:        BSD-3-Clause
Provides:       %{name} = %{version}
Source0:        %{name}-%{version}.tar.gz

%undefine source_date_epoch_from_changelog

%description
ChatOps bot and tooling, for changelog visit https://github.com/hangxie/chatops/releases

%global debug_package %{nil}

%prep
%autosetup

%build
cp /tmp/%{name}.gz %{name}.gz
gunzip %{name}.gz

%install
install -Dpm 0755 %{name} %{buildroot}%{_bindir}/%{name}

%files
%{_bindir}/%{name}
