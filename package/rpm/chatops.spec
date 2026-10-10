Name:           chatops
Version:        CHANGEME
Release:        1%{?dist}
Summary:        ChatOps daemon and built-in MCP server
License:        BSD-3-Clause
Provides:       %{name} = %{version}
Source0:        %{name}-%{version}.tar.gz
BuildRequires:  systemd-rpm-macros
Requires(pre):  shadow-utils
%{?systemd_requires}

%undefine source_date_epoch_from_changelog

%description
ChatOps daemon and built-in MCP server, for changelog visit https://github.com/hangxie/chatops/releases

%global debug_package %{nil}

%prep
%autosetup

%build
for bin in chatops chatops-mcp; do
    cp /tmp/${bin}.gz ${bin}.gz
    gunzip ${bin}.gz
done

%install
install -Dpm 0755 chatops %{buildroot}%{_bindir}/chatops
install -Dpm 0755 chatops-mcp %{buildroot}%{_bindir}/chatops-mcp
install -Dpm 0644 package/systemd/%{name}.service %{buildroot}%{_unitdir}/%{name}.service
install -Dpm 0640 package/systemd/%{name}.env %{buildroot}%{_sysconfdir}/%{name}/%{name}.env
install -Dpm 0640 package/systemd/config.yaml %{buildroot}%{_sysconfdir}/%{name}/config.yaml
install -Dpm 0640 package/systemd/status.yaml %{buildroot}%{_sysconfdir}/%{name}/status.yaml

%pre
getent group %{name} >/dev/null || groupadd -r %{name}
getent passwd %{name} >/dev/null || useradd -r -M -g %{name} -d /nonexistent -s /sbin/nologin -c "ChatOps service" %{name}

%post
%systemd_post %{name}.service

%preun
%systemd_preun %{name}.service

%postun
%systemd_postun_with_restart %{name}.service

%files
%{_bindir}/chatops
%{_bindir}/chatops-mcp
%{_unitdir}/%{name}.service
%dir %attr(0750,root,%{name}) %{_sysconfdir}/%{name}
%config(noreplace) %attr(0640,root,%{name}) %{_sysconfdir}/%{name}/%{name}.env
%config(noreplace) %attr(0640,root,%{name}) %{_sysconfdir}/%{name}/config.yaml
%config(noreplace) %attr(0640,root,%{name}) %{_sysconfdir}/%{name}/status.yaml
