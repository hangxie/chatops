Name:           chatops
Version:        CHANGEME
Release:        1%{?dist}
Summary:        ChatOps bot and tooling
License:        BSD-3-Clause
Provides:       %{name} = %{version}
Source0:        %{name}-%{version}.tar.gz
BuildRequires:  systemd-rpm-macros
Requires(pre):  shadow-utils
%{?systemd_requires}

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
install -Dpm 0644 package/systemd/%{name}.service %{buildroot}%{_unitdir}/%{name}.service
install -Dpm 0640 package/systemd/%{name}.env %{buildroot}%{_sysconfdir}/%{name}/%{name}.env

%pre
getent group %{name} >/dev/null || groupadd -r %{name}
getent passwd %{name} >/dev/null || useradd -r -M -g %{name} -d /nonexistent -s /sbin/nologin -c "ChatOps service" %{name}

%post
%systemd_post chatops.service

%preun
%systemd_preun chatops.service

%postun
%systemd_postun_with_restart chatops.service

%files
%{_bindir}/%{name}
%{_unitdir}/%{name}.service
%dir %attr(0750,root,chatops) %{_sysconfdir}/%{name}
%config(noreplace) %attr(0640,root,chatops) %{_sysconfdir}/%{name}/%{name}.env
